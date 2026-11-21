# latency-mmdb

Builds a MaxMind DB (`.mmdb`) that maps every IP range to its country, continent,
nearest [WonderNetwork](https://wondernetwork.com/pings) city and the median RTT
from that city to every other WonderNetwork city.

## Download

Built by [`.github/workflows/release.yml`](.github/workflows/release.yml) (manual run; tag = DB-IP month, e.g. `2026-11`):

```sh
curl -LO https://github.com/yeganemehr/latency-mmdb/releases/latest/download/latency.mmdb
```

## Build

```sh
curl -LO https://wp-public-data.s3.amazonaws.com/pings/pings-2020-07-19-2020-07-20.csv.gz
curl -LO https://wp-public-data.s3.amazonaws.com/pings/servers-2020-07-19.csv
curl -LO https://download.db-ip.com/free/dbip-city-lite-$(date +%Y-%m).csv.gz

go run . -pings pings-2020-07-19-2020-07-20.csv.gz \
         -servers servers-2020-07-19.csv \
         -geo dbip-city-lite-$(date +%Y-%m).csv.gz \
         -out latency.mmdb
```

About 1 minute and 1 GB RAM; produces a ~55 MB file.

## Record

```json
{
  "country": "PL",
  "continent": "EU",
  "city": "Frankfurt",
  "location": { "latitude": 50.11, "longitude": 8.68 },
  "rtt": { "London": 13, "Istanbul": 35, "Singapore": 150, "...": 0 }
}
```

- `country` / `continent`: the range's own DB-IP country (ISO 3166-1 alpha-2) and continent
  (`AF`, `AN`, `AS`, `EU`, `NA`, `OC`, `SA`). Use these for geo routing, not `city`, which can be
  in another country.
- `city`: nearest WonderNetwork city to the range's DB-IP coordinates.
- `location`: coordinates of `city`, not of the range.
- `rtt`: median of the `avg` ping (ms, both directions) from `city` to each other city.
  The record's own city is absent (treat as best), and a few pairs are missing (treat as unknown).
- Reserved/private ranges and DB-IP `ZZ` rows are not in the DB.

## Usage

Any MaxMind DB reader works. Each example picks the fastest PoP for a client IP
and prints `Johannesburg -> Frankfurt (173 ms)`. Lookups of IPs not in the DB
(private/reserved ranges) return no record.

### PHP

```sh
composer require maxmind-db/reader
```

```php
<?php

require 'vendor/autoload.php';

use MaxMind\Db\Reader;

$reader = new Reader('latency.mmdb');
$record = $reader->get('41.0.0.1');
$reader->close();

$rtt = fn (string $pop) => $pop === $record['city'] ? 0 : ($record['rtt'][$pop] ?? INF);
$pops = ['Frankfurt', 'NewYork', 'Singapore'];
usort($pops, fn ($a, $b) => $rtt($a) <=> $rtt($b));

echo "{$record['city']} -> {$pops[0]} ({$rtt($pops[0])} ms)\n";
```

### JavaScript (Node.js)

```sh
npm install maxmind
```

```js
import maxmind from 'maxmind';

const reader = await maxmind.open('latency.mmdb');
const record = reader.get('41.0.0.1');

const rtt = (pop) => (pop === record.city ? 0 : record.rtt[pop] ?? Infinity);
const [best] = ['Frankfurt', 'NewYork', 'Singapore'].sort((a, b) => rtt(a) - rtt(b));

console.log(`${record.city} -> ${best} (${rtt(best)} ms)`);
```

### Go

```sh
go get github.com/oschwald/maxminddb-golang/v2
```

```go
package main

import (
	"cmp"
	"fmt"
	"log"
	"math"
	"net/netip"
	"slices"

	"github.com/oschwald/maxminddb-golang/v2"
)

type Record struct {
	City string            `maxminddb:"city"`
	RTT  map[string]uint16 `maxminddb:"rtt"`
}

func main() {
	db, err := maxminddb.Open("latency.mmdb")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var r Record
	if err := db.Lookup(netip.MustParseAddr("41.0.0.1")).Decode(&r); err != nil {
		log.Fatal(err)
	}

	rtt := func(pop string) float64 {
		if pop == r.City {
			return 0
		}
		if v, ok := r.RTT[pop]; ok {
			return float64(v)
		}
		return math.Inf(1)
	}
	best := slices.MinFunc([]string{"Frankfurt", "NewYork", "Singapore"}, func(a, b string) int {
		return cmp.Compare(rtt(a), rtt(b))
	})

	fmt.Printf("%s -> %s (%.0f ms)\n", r.City, best, rtt(best))
}
```

### Python

```sh
pip install maxminddb
```

```python
import maxminddb

with maxminddb.open_database("latency.mmdb") as reader:
    record = reader.get("41.0.0.1")

rtt = lambda pop: 0 if pop == record["city"] else record["rtt"].get(pop, float("inf"))
best = min(["Frankfurt", "NewYork", "Singapore"], key=rtt)

print(f"{record['city']} -> {best} ({rtt(best)} ms)")
```

## Data & licensing

- DB-IP Lite: CC BY 4.0, attribution required ("IP Geolocation by DB-IP", https://db-ip.com).
- WonderNetwork pings: no formal license; they ask to credit and link
  wondernetwork.com and to reach out for other uses. 
