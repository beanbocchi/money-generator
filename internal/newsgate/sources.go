package newsgate

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Item is one headline. The field order is the order log records carry these keys in.
type Item struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Title     string `json:"title"`
	Link      string `json:"link"`
	Published string `json:"published"`
}

// fetchError is one source that could not be read this cycle.
type fetchError struct {
	source string
	err    error
}

// maxBody bounds every response body read, from the feeds and from the model.
const maxBody = 8 << 20

// fetchAll reads Binance and then every RSS feed. One bad source does not stop the
// others. The only error returned is ctx's, so a shutdown logs nothing for the
// requests it cut off.
func (g *Gate) fetchAll(ctx context.Context) ([]Item, []fetchError, error) {
	type source struct {
		name, url string
		parse     func([]byte) ([]Item, error)
	}
	sources := []source{{"binance", g.BinanceURL, parseBinance}}
	for _, f := range g.Feeds {
		sources = append(sources, source{f.Name, f.URL, func(b []byte) ([]Item, error) { return parseRSS(f.Name, b) }})
	}
	var items []Item
	var errs []fetchError
	for _, s := range sources {
		raw, err := g.get(ctx, s.url)
		var got []Item
		if err == nil {
			got, err = s.parse(raw)
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err != nil {
			errs = append(errs, fetchError{s.name, err})
			continue
		}
		items = append(items, got...)
	}
	return items, errs, nil
}

// get fetches url with the gate's User-Agent and no compression. Any status outside
// 2xx is an error.
func (g *Gate) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UA)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// parseBinance reads the announcement list, keeping the watched catalogs.
func parseBinance(raw []byte) ([]Item, error) {
	var doc struct {
		Data *struct {
			Catalogs []struct {
				CatalogID float64 `json:"catalogId"`
				Articles  []struct {
					Code        string `json:"code"`
					Title       string `json:"title"`
					ReleaseDate int64  `json:"releaseDate"`
				} `json:"articles"`
			} `json:"catalogs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("binance: %w", err)
	}
	if doc.Data == nil {
		return nil, errors.New("binance: no data")
	}
	var out []Item
	for _, cat := range doc.Data.Catalogs {
		name, ok := binanceCatalogs[int(cat.CatalogID)]
		if !ok {
			continue
		}
		for _, a := range cat.Articles {
			if a.Code == "" || a.ReleaseDate == 0 {
				return nil, fmt.Errorf("binance: article %q lacks code or releaseDate", a.Title)
			}
			out = append(out, Item{
				ID:        "binance:" + a.Code,
				Source:    "binance/" + name,
				Title:     a.Title,
				Link:      "https://www.binance.com/en/support/announcement/" + a.Code,
				Published: isoUTC(time.UnixMilli(a.ReleaseDate)),
			})
		}
	}
	return out, nil
}

// rssItem is an <item>'s children; only those without a namespace are read.
type rssItem struct {
	Children []struct {
		XMLName xml.Name
		Text    string `xml:",chardata"`
	} `xml:",any"`
}

// text is the first child named tag, "" if there is none.
func (it *rssItem) text(tag string) string {
	for _, c := range it.Children {
		if c.XMLName == (xml.Name{Local: tag}) {
			return c.Text
		}
	}
	return ""
}

// parseRSS reads every <item> without a namespace, in document order. Items without a
// title or pubDate are skipped; a pubDate that cannot be parsed fails the whole feed.
func parseRSS(name string, raw []byte) ([]Item, error) {
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	var out []Item
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name != (xml.Name{Local: "item"}) {
			continue
		}
		var it rssItem
		if err := d.DecodeElement(&it, &se); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		title, link, pub := strings.TrimSpace(it.text("title")), strings.TrimSpace(it.text("link")), it.text("pubDate")
		if title == "" || pub == "" {
			continue
		}
		t, err := parseDate(pub)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, Item{
			ID:        name + ":" + strings.TrimSpace(cmp.Or(it.text("guid"), link, title)),
			Source:    name,
			Title:     title,
			Link:      link,
			Published: isoUTC(t),
		})
	}
}

// dateLayouts are the RFC 822/1123 forms RSS pubDates use (time.RFC1123Z and
// time.RFC1123 with one- or two-digit days, with and without the weekday), plus
// RFC 822Z and RFC 850. A zone abbreviation Go does not know reads as UTC.
var dateLayouts = []string{
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 MST",
	time.RFC822Z, time.RFC850,
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q", s)
}
