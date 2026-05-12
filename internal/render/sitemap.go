package render

import (
	"bytes"
	"encoding/xml"
	"sort"
	"time"
)

// SiteURLEntry is one row in sitemap.xml.
type SiteURLEntry struct {
	XMLName xml.Name `xml:"url"`
	Loc     string   `xml:"loc"`
	LastMod string   `xml:"lastmod,omitempty"`
}

// URLSet is the root <urlset> element for sitemap.xml.
type URLSet struct {
	XMLName xml.Name       `xml:"urlset"`
	Xmlns   string         `xml:"xmlns,attr"`
	URLs    []SiteURLEntry `xml:"url"`
}

// BuildSitemap renders sitemap.xml bytes from the page set. baseURL is the
// canonical site URL (e.g. https://wiki.lilbro.cloud); paths are appended
// to it. Pages are sorted by URL for reproducibility.
func BuildSitemap(pages []*Page, baseURL string) ([]byte, error) {
	entries := make([]SiteURLEntry, 0, len(pages))
	for _, p := range pages {
		e := SiteURLEntry{Loc: baseURL + p.RelativeURL}
		if !p.Modified.IsZero() {
			e.LastMod = p.Modified.UTC().Format(time.RFC3339)
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Loc < entries[j].Loc })
	doc := URLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: entries}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// RSSItem is one item in the RSS feed.
type RSSItem struct {
	XMLName     xml.Name `xml:"item"`
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description,omitempty"`
	PubDate     string   `xml:"pubDate,omitempty"`
	GUID        string   `xml:"guid"`
}

// RSSChannel is the <channel> wrapping the items.
type RSSChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []RSSItem `xml:"item"`
}

// RSS is the root <rss version="2.0"> element.
type RSS struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel RSSChannel `xml:"channel"`
}

// BuildRSS renders index.xml bytes. Includes the 50 most recently
// modified pages, sorted descending — Quartz's default.
func BuildRSS(pages []*Page, baseURL, siteTitle, siteDesc string) ([]byte, error) {
	sorted := make([]*Page, 0, len(pages))
	sorted = append(sorted, pages...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Modified.After(sorted[j].Modified)
	})
	if len(sorted) > 50 {
		sorted = sorted[:50]
	}
	items := make([]RSSItem, 0, len(sorted))
	for _, p := range sorted {
		it := RSSItem{
			Title:       p.Title,
			Link:        baseURL + p.RelativeURL,
			Description: p.Description,
			GUID:        baseURL + p.RelativeURL,
		}
		if !p.Modified.IsZero() {
			it.PubDate = p.Modified.UTC().Format(time.RFC1123Z)
		}
		items = append(items, it)
	}
	doc := RSS{
		Version: "2.0",
		Channel: RSSChannel{
			Title:       siteTitle,
			Link:        baseURL,
			Description: siteDesc,
			Items:       items,
		},
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
