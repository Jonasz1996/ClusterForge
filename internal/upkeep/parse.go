package upkeep

import (
	"regexp"
	"strconv"
	"strings"
)

// Result is wat één beurt op één node deed of, bij een voorbeeld, zou doen.
type Result struct {
	// Skipped zegt waarom de node niets deed, zoals "geen Linux".
	Skipped    string     `json:"skipped,omitempty"`
	UsedBefore int64      `json:"used_before"`
	UsedAfter  int64      `json:"used_after"`
	Freed      int64      `json:"freed"`
	Items      []ItemRes  `json:"items"`
	DB         []DBRes    `json:"db"`
	Tables     []TableRes `json:"tables"`
	// Order is de plaats van de node in de beurt, vanaf 1.
	Order int `json:"order,omitempty"`
	// Why zegt waarom de node op die plaats staat.
	Why string `json:"why,omitempty"`
}

// ItemRes is één schijfonderdeel.
type ItemRes struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Status: ok, fail, skip, of "plan" in een voorbeeld.
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	// Bytes: wat er vrij kan komen (bij Docker een schatting).
	Bytes int64 `json:"bytes"`
}

// DBRes is wat er met een database gebeurde.
type DBRes struct {
	Engine string `json:"engine"`
	Role   string `json:"role"`
	Action string `json:"action"`
	Count  int    `json:"count"`
	Note   string `json:"note"`
}

// TableRes is een gefragmenteerde tabel of opgezwollen index.
type TableRes struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Free   int64  `json:"free"`
	Size   int64  `json:"size"`
}

// OK is false als iets mislukte.
func (r Result) OK() bool {
	for _, it := range r.Items {
		if it.Status == "fail" {
			return false
		}
	}
	for _, d := range r.DB {
		if d.Action == "fail" {
			return false
		}
	}
	return true
}

// Parse leest de CFUPKEEP-regels.
func Parse(lines []string, preview bool) Result {
	r := Result{Items: []ItemRes{}, DB: []DBRes{}, Tables: []TableRes{}}
	idx := map[string]int{}
	get := func(id string) *ItemRes {
		i, ok := idx[id]
		if !ok {
			st := "plan"
			if !preview {
				st = "ok"
			}
			r.Items = append(r.Items, ItemRes{ID: id, Title: Title(id), Status: st})
			i = len(r.Items) - 1
			idx[id] = i
		}
		return &r.Items[i]
	}
	num := func(s string) int64 {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 2 || f[0] != "CFUPKEEP" {
			continue
		}
		switch f[1] {
		case "skip":
			if len(f) >= 3 {
				r.Skipped = f[2]
			}
		case "used":
			if len(f) < 4 {
				continue
			}
			if f[2] == "before" {
				r.UsedBefore = num(f[3])
			} else {
				r.UsedAfter = num(f[3])
			}
		case "size":
			if len(f) >= 4 {
				get(f[2]).Bytes = num(f[3])
			}
		case "dsize":
			if len(f) >= 4 {
				get(f[2]).Bytes = parseHuman(f[3])
			}
		case "item":
			if len(f) >= 4 {
				it := get(f[2])
				it.Status = f[3]
				if len(f) >= 5 {
					it.Note = f[4]
				}
			}
		case "db":
			if len(f) >= 7 {
				n, _ := strconv.Atoi(f[5])
				r.DB = append(r.DB, DBRes{Engine: f[2], Role: f[3], Action: f[4], Count: n, Note: f[6]})
			}
		case "table":
			if len(f) >= 6 && len(r.Tables) < 200 {
				r.Tables = append(r.Tables, TableRes{Engine: f[2], Name: f[3], Free: num(f[4]), Size: num(f[5])})
			}
		}
	}
	if preview {
		for _, it := range r.Items {
			r.Freed += it.Bytes
		}
	} else if r.UsedBefore > 0 && r.UsedAfter > 0 && r.UsedBefore > r.UsedAfter {
		r.Freed = r.UsedBefore - r.UsedAfter
	}
	return r
}

var humanRe = regexp.MustCompile(`^\s*([0-9.]+)\s*([kKMGTP]?i?B)?`)

// parseHuman leest "1.2GB (40%)" zoals docker system df het toont.
func parseHuman(s string) int64 {
	m := humanRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"": 1, "B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	if mult == 0 {
		mult = 1
	}
	return int64(v * mult)
}
