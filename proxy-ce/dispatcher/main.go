package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"github.com/aws/aws-lambda-go/events"
	fn "github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/golang/groupcache/lru"
	"github.com/oschwald/maxminddb-golang"
	"github.com/ua-parser/uap-go/uaparser"
)

type GeoIP struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

type GeoIDKey struct {
	Country string
	City    string
}

type InEvent struct {
	Timestamp int64
	IP        string
	UA        string
	Payload   string
}

type OutEvent struct {
	Timestamp int64
	IID       string
	OID       *string
	GeoID     string
	UA        string
	Payload   map[string]string
}

type Mapping struct {
	IID string
	OID string
}

var (
	functionName  = os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
	mappingsTable = os.Getenv("MAPPINGS_TABLE")
	uaPropertyID  = os.Getenv("UA_PROPERTY_ID")

	//go:embed data/geoip_20210216.mmdb
	geoIPRaw []byte
	geoIPs   *maxminddb.Reader
	ipv4Mask = net.CIDRMask(24, 32)
	ipv6Mask = net.CIDRMask(48, 128)

	//go:embed data/geoid_20201118.gob
	geoIDRaw   []byte
	geoIDs     map[GeoIDKey]string
	geoIDCache = lru.New(100000)

	//go:embed data/ua_20210213.yaml
	uaRaw   []byte
	uap     *uaparser.Parser
	uaCache = lru.New(100000)
)

const oidSize = 16

func main() {
	var err error

	if geoIPs, err = maxminddb.FromBytes(geoIPRaw); err != nil {
		log.Fatal(err)
	}

	dec := gob.NewDecoder(bytes.NewReader(geoIDRaw))
	if err = dec.Decode(&geoIDs); err != nil {
		log.Fatal(err)
	}

	if uap, err = uaparser.NewFromBytes(uaRaw); err != nil {
		log.Fatal(err)
	}

	fn.Start(Handle)
}

func Handle(ctx context.Context, in *events.KinesisEvent) error {
	cfg, _ := config.LoadDefaultConfig(ctx)
	dynsvc := dynamodb.NewFromConfig(cfg)

	// !!! Community Edition (CE) - ✘ HSM !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	seedh := sha256.New()
	io.WriteString(seedh, functionName)
	io.WriteString(seedh, time.Now().UTC().Format("2006-01-02"))
	hrand := mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(seedh.Sum(nil)))))
	var hkey [32]byte
	_, err := hrand.Read(hkey[:])
	if err != nil {
		log.Println(err)
		return err
	}
	// !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	var outEvts []*OutEvent
	for _, rec := range in.Records {
		var inEvt InEvent
		if err := json.Unmarshal(rec.Kinesis.Data, &inEvt); err != nil {
			continue
		}
		outEvt := decode(&inEvt, uaPropertyID, hkey[:])
		if outEvt == nil {
			continue
		}
		outEvts = append(outEvts, outEvt)
	}
	if len(outEvts) == 0 {
		return nil
	}

	if err := hydrate(ctx, dynsvc, mappingsTable, outEvts); err != nil {
		log.Println(err)
		return err
	}
	if err := populate(ctx, dynsvc, mappingsTable, outEvts); err != nil {
		log.Println(err)
		return err
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	var data []string
	for _, evt := range outEvts {
		data = append(data, encode(evt, uaPropertyID, now))
	}

	body := strings.NewReader(strings.Join(data, "\n"))
	_, err = http.Post("https://www.google-analytics.com/batch", "text/plain; charset=UTF-8", body)
	if err != nil {
		log.Println(err)
	}
	return err
}

func decode(evt *InEvent, pid string, hkey []byte) *OutEvent {
	iidh := hmac.New(sha256.New, hkey)
	iidh.Write([]byte(pid))
	iidh.Write([]byte(evt.IP))
	iidh.Write([]byte(evt.UA))
	iid := hex.EncodeToString(iidh.Sum(nil))

	geoID := locateIP(evt.IP)
	ua := redactUA(evt.UA)

	params, err := url.ParseQuery(evt.Payload)
	if err != nil {
		return nil
	}
	pld := make(map[string]string)
	for k, v := range params {
		if len(v) == 1 && len(v[0]) > 0 {
			pld[k] = v[0]
		}
	}

	if typ, ok := pld["type"]; !ok || typ != "page" {
		return nil
	}

	return &OutEvent{Timestamp: evt.Timestamp, IID: iid, GeoID: geoID, UA: ua, Payload: pld}
}

func encode(evt *OutEvent, pid string, now int64) string {
	params := url.Values{}
	params.Add("v", "1")
	params.Add("tid", pid)
	params.Add("cid", *evt.OID)
	params.Add("qt", strconv.FormatInt(now-evt.Timestamp, 10))
	params.Add("aip", "1")
	params.Add("geoid", evt.GeoID)
	params.Add("ua", evt.UA)
	params.Add("t", "pageview")
	if dt, ok := evt.Payload["title"]; ok {
		params.Add("dt", dt)
	}
	if dl, ok := evt.Payload["url"]; ok {
		params.Add("dl", dl)
		if dr, ok := evt.Payload["referrer"]; ok {
			if !strings.HasPrefix(dl, dr) {
				params.Add("dr", dr)
			}
		}
	}
	return params.Encode()
}

func hydrate(ctx context.Context, dynsvc *dynamodb.Client, table string, evts []*OutEvent) error {
	var keys []map[string]types.AttributeValue
	seen := make(map[string]struct{})
	for _, evt := range evts {
		if _, ok := seen[evt.IID]; ok {
			continue
		}
		key, _ := attributevalue.MarshalMap(map[string]interface{}{"IID": evt.IID})
		keys = append(keys, key)
		seen[evt.IID] = struct{}{}
	}

	bgi, err := dynsvc.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]types.KeysAndAttributes{table: {Keys: keys}},
	})
	if err != nil {
		return err
	}

	mappings := make(map[string]*string)
	for _, row := range bgi.Responses[table] {
		var m Mapping
		attributevalue.UnmarshalMap(row, &m)
		mappings[m.IID] = &m.OID
	}
	for _, evt := range evts {
		evt.OID = mappings[evt.IID]
	}

	return nil
}

func populate(ctx context.Context, dynsvc *dynamodb.Client, table string, evts []*OutEvent) error {
	var items []types.WriteRequest
	seen := make(map[string]string)
	for _, evt := range evts {
		if oid, ok := seen[evt.IID]; ok {
			evt.OID = &oid
			continue
		}
		if evt.OID == nil {
			oid, err := generateOID()
			if err != nil {
				return err
			}
			ttl := time.Unix(0, evt.Timestamp*int64(time.Millisecond)).Truncate(24 * time.Hour).Add(24*time.Hour + 15*time.Minute).Unix()
			item, _ := attributevalue.MarshalMap(map[string]interface{}{"IID": evt.IID, "OID": oid, "TTL": ttl})
			items = append(items, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
			evt.OID = &oid
		}
		seen[evt.IID] = *evt.OID
	}

	if len(items) == 0 {
		return nil
	}

	_, err := dynsvc.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
		RequestItems: map[string][]types.WriteRequest{table: items},
	})
	return err
}

func generateOID() (string, error) {
	// !!! Community Edition (CE) - ✘ HSM !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	var id [oidSize]byte
	_, err := crand.Read(id[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
	// !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
}

func locateIP(ip string) string {
	if cached, ok := geoIDCache.Get(ip); ok {
		return cached.(string)
	}

	aip := net.ParseIP(ip)
	if aip.To4() != nil {
		aip = aip.Mask(ipv4Mask)
	} else {
		aip = aip.Mask(ipv6Mask)
	}

	var geoIP GeoIP
	if err := geoIPs.Lookup(aip, &geoIP); err != nil {
		return "XX"
	}
	country, city := geoIP.Country.ISOCode, geoIP.City.Names["en"]
	geoID, ok := geoIDs[GeoIDKey{country, city}]
	if !ok {
		geoID = country
	}

	geoIDCache.Add(ip, geoID)
	return geoID
}

func redactUA(ua string) string {
	if cached, ok := uaCache.Get(ua); ok {
		return cached.(string)
	}

	browser := uap.ParseUserAgent(ua)
	os := uap.ParseOs(ua)
	redacted := fmt.Sprintf("%s (%s; U)", browser.Family, os.Family)

	uaCache.Add(ua, redacted)
	return redacted
}
