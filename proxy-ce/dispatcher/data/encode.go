package main

import (
	"bytes"
	"encoding/csv"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"unicode"
)

const (
	dbName = "geoid_20201118"
)

type Key struct {
	Country string
	City    string
}

func main() {
	in, err := os.OpenFile(fmt.Sprintf("%s.csv", dbName), os.O_RDONLY, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer in.Close()

	r := csv.NewReader(in)
	rows, err := r.ReadAll()
	if err != nil {
		log.Fatal(err)
	}

	recs := make(map[Key]string)
	rows = rows[1:]
	for _, row := range rows {
		cid, country, city := row[0], row[4], row[1]
		if !unicode.IsLetter(rune(city[0])) {
			continue
		}
		recs[Key{country, city}] = cid
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(recs); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(fmt.Sprintf("%s.gob", dbName), buf.Bytes(), 0644); err != nil {
		log.Fatal(err)
	}
}
