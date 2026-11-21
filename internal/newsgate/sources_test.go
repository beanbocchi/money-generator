package newsgate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const pubA = "Mon, 16 Nov 2026 10:00:00 +0000"

type rssCase struct {
	name    string
	doc     string
	want    []Item
	wantErr bool
}

var rssCases = []rssCase{
	{"guid falls back to link, then title",
		`<rss><channel>
			<item><title> T1 </title><link> https://l/1 </link><pubDate>` + pubA + `</pubDate></item>
			<item><title>T2</title><guid></guid><pubDate>` + pubA + `</pubDate></item>
			<item><title>T3</title><guid>  </guid><link>https://l/3</link><pubDate>` + pubA + `</pubDate></item>
			</channel></rss>`,
		[]Item{
			{"f:https://l/1", "f", "T1", "https://l/1", "2026-11-16T10:00:00+00:00"},
			{"f:T2", "f", "T2", "", "2026-11-16T10:00:00+00:00"},
			{"f:", "f", "T3", "https://l/3", "2026-11-16T10:00:00+00:00"},
		}, false},
	{"items without title or pubDate are skipped",
		`<rss><item><title></title><pubDate>` + pubA + `</pubDate></item><item><title>x</title></item>
			<item><title>y</title><pubDate></pubDate></item></rss>`, nil, false},
	{"one bad pubDate fails the feed",
		`<rss><item><title>a</title><pubDate>` + pubA + `</pubDate></item><item><title>b</title><pubDate>yesterday</pubDate></item></rss>`,
		nil, true},
	{"namespaced children and items do not count",
		`<rss xmlns:atom="http://www.w3.org/2005/Atom" xmlns:x="urn:x"><item><atom:link>https://a</atom:link><title>a</title>
			<guid>g</guid><pubDate>` + pubA + `</pubDate></item><x:item><title>b</title><pubDate>` + pubA + `</pubDate></x:item></rss>`,
		[]Item{{"f:g", "f", "a", "", "2026-11-16T10:00:00+00:00"}}, false},
	{"items in a default namespace do not count",
		`<rdf xmlns="http://purl.org/rss/1.0/"><item><title>a</title><pubDate>` + pubA + `</pubDate></item></rdf>`, nil, false},
	{"CDATA and child text",
		`<rss><item><title>outer <b>bold</b> tail</title><pubDate>` + pubA + `</pubDate></item>
			<item><title><![CDATA[inner & <cdata>]]></title><pubDate>` + pubA + `</pubDate></item></rss>`,
		[]Item{{"f:outer  tail", "f", "outer  tail", "", "2026-11-16T10:00:00+00:00"},
			{"f:inner & <cdata>", "f", "inner & <cdata>", "", "2026-11-16T10:00:00+00:00"}}, false},
	{"malformed XML fails", `<rss><item></rss>`, nil, true},
	{"undefined entity fails", `<rss><item><title>&nbsp;</title></item></rss>`, nil, true},
	{"undeclared encoding fails", `<?xml version="1.0" encoding="windows-1252"?><rss/>`, nil, true},
	{"BOM", "\xef\xbb\xbf<rss/>", nil, false},
}

func TestParseRSS(t *testing.T) {
	tests := rssCases
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRSS("f", []byte(tt.doc))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

var binanceCases = []rssCase{
	{"watched catalogs only",
		`{"data": {"catalogs": [
			{"catalogId": 161.0, "articles": [{"code": "c1", "title": "D", "releaseDate": 1763460000999}]},
			{"catalogId": 48, "articles": [{"code": "c2", "title": "L", "releaseDate": 1763460000999}]},
			{"catalogId": 157, "articles": [{"code": "42", "title": "M", "releaseDate": 1763460001000}]}]}}`,
		[]Item{
			{"binance:c1", "binance/Delisting", "D", "https://www.binance.com/en/support/announcement/c1", "2025-11-18T10:00:00+00:00"},
			{"binance:42", "binance/Maintenance Updates", "M", "https://www.binance.com/en/support/announcement/42", "2025-11-18T10:00:01+00:00"},
		}, false},
	{"no data", `{"code": "000000"}`, nil, true},
	{"string catalog id", `{"data": {"catalogs": [{"catalogId": "49", "articles": []}]}}`, nil, true},
	{"article without code", `{"data": {"catalogs": [{"catalogId": 49, "articles": [{"title": "x", "releaseDate": 1}]}]}}`, nil, true},
	{"article without releaseDate", `{"data": {"catalogs": [{"catalogId": 49, "articles": [{"code": "x", "title": "x"}]}]}}`, nil, true},
	{"not json", `<html>`, nil, true},
}

func TestParseBinance(t *testing.T) {
	tests := binanceCases
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBinance([]byte(tt.doc))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestFetchErrorWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	g := &Gate{HTTP: srv.Client()}
	_, err := g.get(context.Background(), srv.URL)
	if err == nil || err.Error() != "HTTP 500 Internal Server Error" {
		t.Errorf("err = %v", err)
	}
}

func TestParseDate(t *testing.T) {
	want := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, s := range []string{
		"Fri, 06 Nov 2026 10:00:00 +0000", "Fri, 6 Nov 2026 12:00:00 +0200", " Fri, 06 Nov 2026 10:00:00 GMT\n",
		"06 Nov 2026 05:00:00 -0500", "6 Nov 2026 10:00:00 UTC", "06 Nov 26 10:00 +0000", "Friday, 06-Nov-26 10:00:00 GMT",
	} {
		if got, err := parseDate(s); err != nil || !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, %v", s, got, err)
		}
	}
	if _, err := parseDate("yesterday"); err == nil {
		t.Error("garbage date accepted")
	}
}

func TestErrorTextIsCut(t *testing.T) {
	long := strings.Repeat("é", 400)
	if got := errorText(errString(long)); len([]rune(got)) != 300 {
		t.Errorf("errorText kept %d runes, want 300", len([]rune(got)))
	}
}

type errString string

func (e errString) Error() string { return string(e) }
