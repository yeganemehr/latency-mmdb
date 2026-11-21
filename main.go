package main

import (
	"compress/gzip"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"go4.org/netipx"
)

type city struct {
	name     string
	lat, lon float64
}

type stats struct {
	rows, skipped, ranges, prefixes, reserved int
}

func main() {
	pings := flag.String("pings", "", "WonderNetwork pings CSV (.csv or .csv.gz)")
	servers := flag.String("servers", "", "WonderNetwork servers CSV")
	geo := flag.String("geo", "", "DB-IP Lite City CSV (.csv or .csv.gz)")
	out := flag.String("out", "latency.mmdb", "output mmdb path")
	flag.Parse()
	if *pings == "" || *servers == "" || *geo == "" {
		flag.Usage()
		os.Exit(2)
	}

	cities, err := loadCities(*servers)
	if err != nil {
		log.Fatal(err)
	}
	rtt, err := loadRTT(*pings, cities)
	if err != nil {
		log.Fatal(err)
	}
	tree, st, err := build(*geo, cities, rtt)
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tree.WriteTo(f); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	log.Printf("cities=%d pairs=%d geo_rows=%d skipped=%d ranges=%d prefixes=%d reserved=%d -> %s",
		len(cities), len(rtt), st.rows, st.skipped, st.ranges, st.prefixes, st.reserved, *out)
}

func openCSV(path string) (*csv.Reader, io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		r = gz
	}
	c := csv.NewReader(r)
	c.ReuseRecord = true
	c.FieldsPerRecord = -1
	return c, f, nil
}

func loadCities(path string) (map[string]city, error) {
	r, c, err := openCSV(path)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	out := map[string]city{}
	header := true
	for {
		row, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if header {
			header = false
			continue
		}
		lat, err1 := strconv.ParseFloat(row[8], 64)
		lon, err2 := strconv.ParseFloat(row[9], 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("servers: bad coordinates for id %s", row[0])
		}
		out[row[0]] = city{name: row[1], lat: lat, lon: lon}
	}
}

func pairKey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

func loadRTT(path string, cities map[string]city) (map[[2]string]uint16, error) {
	r, c, err := openCSV(path)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	samples := map[[2]string][]float64{}
	header := true
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header {
			header = false
			continue
		}
		src, ok1 := cities[row[0]]
		dst, ok2 := cities[row[1]]
		avg, err := strconv.ParseFloat(row[4], 64)
		if !ok1 || !ok2 || src.name == dst.name || err != nil {
			continue
		}
		k := pairKey(src.name, dst.name)
		samples[k] = append(samples[k], avg)
	}

	out := make(map[[2]string]uint16, len(samples))
	for k, v := range samples {
		slices.Sort(v)
		out[k] = uint16(min(math.Round(v[len(v)/2]), math.MaxUint16))
	}
	return out, nil
}

func records(cities map[string]city, rtt map[[2]string]uint16) map[string]mmdbtype.Map {
	out := map[string]mmdbtype.Map{}
	for _, a := range cities {
		m := mmdbtype.Map{}
		for _, b := range cities {
			if v, ok := rtt[pairKey(a.name, b.name)]; ok {
				m[mmdbtype.String(b.name)] = mmdbtype.Uint16(v)
			}
		}
		out[a.name] = mmdbtype.Map{
			"city": mmdbtype.String(a.name),
			"location": mmdbtype.Map{
				"latitude":  mmdbtype.Float64(a.lat),
				"longitude": mmdbtype.Float64(a.lon),
			},
			"rtt": m,
		}
	}
	return out
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 6371 * 2 * math.Asin(math.Sqrt(h))
}

// ponytail: nearest city with no distance cap, so far-off regions still get a (coarse) answer; add a -max-km cutoff if that misroutes
func nearest(cities map[string]city, lat, lon float64) string {
	best, bestKm := "", math.Inf(1)
	for _, c := range cities {
		if km := haversine(lat, lon, c.lat, c.lon); km < bestKm {
			best, bestKm = c.name, km
		}
	}
	return best
}

type recordKey struct{}

func (recordKey) Key(v mmdbtype.DataType) ([]byte, error) {
	m := v.(mmdbtype.Map)
	return []byte(string(m["city"].(mmdbtype.String)) + "|" + string(m["country"].(mmdbtype.String)) + "|" + string(m["continent"].(mmdbtype.String))), nil
}

func build(geoPath string, cities map[string]city, rtt map[[2]string]uint16) (*mmdbwriter.Tree, stats, error) {
	var st stats
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		KeyGenerator: recordKey{},
		DatabaseType: "Liteium-Latency",
		Description: map[string]string{
			"en": "Country, continent, nearest WonderNetwork city and median RTT (ms) to every other city. Data: WonderNetwork (wondernetwork.com), DB-IP Lite (db-ip.com, CC BY 4.0).",
		},
		RecordSize: 28,
	})
	if err != nil {
		return nil, st, err
	}

	base := records(cities, rtt)
	recs := map[string]mmdbtype.Map{}
	cache := map[[2]float64]string{}

	var cur netipx.IPRange
	var curID string
	flush := func() error {
		if curID == "" {
			return nil
		}
		st.ranges++
		for _, p := range cur.Prefixes() {
			st.prefixes++
			err := tree.Insert(netipx.PrefixIPNet(p), recs[curID])
			var rErr *mmdbwriter.ReservedNetworkError
			var aErr *mmdbwriter.AliasedNetworkError
			if errors.As(err, &rErr) || errors.As(err, &aErr) {
				st.reserved++
				continue
			}
			if err != nil {
				return err
			}
		}
		return nil
	}

	r, c, err := openCSV(geoPath)
	if err != nil {
		return nil, st, err
	}
	defer c.Close()

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, st, err
		}
		st.rows++
		start, err1 := netip.ParseAddr(row[0])
		end, err2 := netip.ParseAddr(row[1])
		lat, err3 := strconv.ParseFloat(row[6], 64)
		lon, err4 := strconv.ParseFloat(row[7], 64)
		if row[3] == "ZZ" || errors.Join(err1, err2, err3, err4) != nil {
			st.skipped++
			continue
		}

		key := [2]float64{lat, lon}
		name, ok := cache[key]
		if !ok {
			name = nearest(cities, lat, lon)
			cache[key] = name
		}

		id := name + "|" + row[3] + "|" + row[2]
		if _, ok := recs[id]; !ok {
			rec := mmdbtype.Map{"country": mmdbtype.String(row[3]), "continent": mmdbtype.String(row[2])}
			for k, v := range base[name] {
				rec[k] = v
			}
			recs[id] = rec
		}

		if id == curID && cur.To().Next() == start {
			cur = netipx.IPRangeFrom(cur.From(), end)
			continue
		}
		if err := flush(); err != nil {
			return nil, st, err
		}
		cur, curID = netipx.IPRangeFrom(start, end), id
	}
	if err := flush(); err != nil {
		return nil, st, err
	}
	return tree, st, nil
}
