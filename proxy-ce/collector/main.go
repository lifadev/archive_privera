package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/events"
	fn "github.com/aws/aws-lambda-go/lambda"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
)

var (
	eventsStream = os.Getenv("EVENTS_STREAM")
)

type Event struct {
	Timestamp int64
	IP        string
	UA        string
	Payload   string
}

func main() {
	fn.Start(Handle)
}

func Handle(ctx context.Context, in *events.APIGatewayV2HTTPRequest) (*events.APIGatewayV2HTTPResponse, error) {
	cfg, _ := config.LoadDefaultConfig(ctx)
	kinsvc := kinesis.NewFromConfig(cfg)

	ip := in.RequestContext.HTTP.SourceIP
	ua := in.RequestContext.HTTP.UserAgent

	evt := &Event{
		Timestamp: in.RequestContext.TimeEpoch,
		IP:        ip,
		UA:        ua,
		Payload:   in.Body,
	}

	pkh := md5.New()
	io.WriteString(pkh, ip)
	io.WriteString(pkh, ua)
	pk := string(pkh.Sum(nil))
	data, _ := json.Marshal(evt)

	kinsvc.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   &eventsStream,
		PartitionKey: &pk,
		Data:         data,
	})

	return &events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"access-control-allow-origin":  "*",
			"cache-control":                "no-cache, no-store, must-revalidate",
			"content-type":                 "image/gif",
			"cross-origin-resource-policy": "cross-origin",
			"expires":                      "Thu, 21 Feb 2013 00:00:00 GMT",
			"pragma":                       "no-cache",
			"server":                       "Privera Community Edition (CE)",
			"x-content-type-options":       "nosniff",
		},
		Body:            "R0lGODlhAQABAID/AP///wAAACwAAAAAAQABAAACAkQBADs=",
		IsBase64Encoded: true,
	}, nil
}
