package main

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/oschwald/maxminddb-golang/v2"
)

type rec struct {
	Country   string            `maxminddb:"country"`
	Continent string            `maxminddb:"continent"`
	City      string            `maxminddb:"city"`
	RTT       map[string]uint16 `maxminddb:"rtt"`
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	data := []byte(body)
	if filepath.Ext(name) == ".gz" {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		gz.Write(data)
		gz.Close()
		data = b.Bytes()
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuild(t *testing.T) {
	servers := writeFile(t, "servers.csv", `"id","name","title","location","state","country","state_abbv","continent","latitude","longitude"
"1","Frankfurt","Frankfurt","Frankfurt","Hesse","Germany","HE","3","50.11","8.68"
"2","London","London","London","England","United Kingdom","EN","3","51.51","-0.13"
"3","Singapore","Singapore","Singapore","","Singapore","","5","1.35","103.82"
`)
	pings := writeFile(t, "pings.csv.gz", `"source","destination","timestamp","min","avg","max","mdev"
"1","2","t","10","14","20","1"
"2","1","t","10","16","20","1"
"1","2","t","10","90","95","1"
"1","3","t","150","160","170","1"
"2","3","t","170","180","190","1"
"9","1","t","1","1","1","1"
`)
	geo := writeFile(t, "geo.csv.gz", `0.0.0.0,0.255.255.255,ZZ,ZZ,,,0,0
1.0.0.0,1.0.0.255,EU,FR,,Paris,48.85,2.35
1.0.1.0,1.0.1.255,EU,DE,,Berlin,52.52,13.40
1.0.2.0,1.0.2.255,EU,DE,,Berlin,52.52,13.40
1.0.3.0,1.0.3.255,EU,PL,,Berlin,52.52,13.40
192.168.0.0,192.168.255.255,EU,DE,,Berlin,52.52,13.40
2a00::,2a00::ffff,AS,MY,,Kuala Lumpur,3.14,101.69
`)

	cities, err := loadCities(servers)
	if err != nil {
		t.Fatal(err)
	}
	rtt, err := loadRTT(pings, cities)
	if err != nil {
		t.Fatal(err)
	}
	tree, st, err := build(geo, cities, rtt)
	if err != nil {
		t.Fatal(err)
	}
	if st.skipped != 1 || st.ranges != 5 || st.reserved == 0 {
		t.Fatalf("stats = %+v", st)
	}

	out := filepath.Join(t.TempDir(), "latency.mmdb")
	f, _ := os.Create(out)
	if _, err := tree.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	db, err := maxminddb.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cases := map[string]rec{
		"1.0.0.7":  {Country: "FR", Continent: "EU", City: "London", RTT: map[string]uint16{"Frankfurt": 16, "Singapore": 180}},
		"1.0.2.9":  {Country: "DE", Continent: "EU", City: "Frankfurt", RTT: map[string]uint16{"London": 16, "Singapore": 160}},
		"1.0.3.9":  {Country: "PL", Continent: "EU", City: "Frankfurt", RTT: map[string]uint16{"London": 16, "Singapore": 160}},
		"2a00::42": {Country: "MY", Continent: "AS", City: "Singapore", RTT: map[string]uint16{"Frankfurt": 160, "London": 180}},
	}
	for ip, want := range cases {
		var got rec
		if err := db.Lookup(netip.MustParseAddr(ip)).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Country != want.Country || got.Continent != want.Continent || got.City != want.City || len(got.RTT) != len(want.RTT) {
			t.Fatalf("%s: got %+v want %+v", ip, got, want)
		}
		for k, v := range want.RTT {
			if got.RTT[k] != v {
				t.Fatalf("%s: rtt[%s]=%d want %d", ip, k, got.RTT[k], v)
			}
		}
	}

	for _, ip := range []string{"192.168.1.1", "0.0.0.1", "8.8.8.8"} {
		if db.Lookup(netip.MustParseAddr(ip)).Found() {
			t.Fatalf("%s should not be found", ip)
		}
	}
}
