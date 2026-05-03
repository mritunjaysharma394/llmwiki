package ingest

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

var (
	reTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reScript   = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)
	reStyle    = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	reSpace    = regexp.MustCompile(`[ \t]+`)
	reNewlines = regexp.MustCompile(`\n{3,}`)
)

func FetchURL(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		return htmlToText(string(body)), nil
	}
	return string(body), nil
}

func htmlToText(html string) string {
	text := reScript.ReplaceAllString(html, " ")
	text = reStyle.ReplaceAllString(text, " ")
	text = reTag.ReplaceAllString(text, " ")
	text = decodeHTMLEntities(text)
	lines := strings.Split(text, "\n")
	var clean []string
	for _, line := range lines {
		line = reSpace.ReplaceAllString(line, " ")
		line = strings.TrimFunc(line, unicode.IsSpace)
		if line != "" {
			clean = append(clean, line)
		}
	}
	result := strings.Join(clean, "\n")
	result = reNewlines.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

func decodeHTMLEntities(s string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
		"&mdash;", "—",
		"&ndash;", "–",
		"&hellip;", "…",
	)
	return replacer.Replace(s)
}
