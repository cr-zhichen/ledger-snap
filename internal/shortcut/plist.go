package shortcut

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
)

type dict = map[string]any

// encodePlist writes v as an XML property list. It covers what the shortcut
// format uses: dict, []any, string, int, float64 and bool.
func encodePlist(v any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	if err := writeValue(&b, v); err != nil {
		return nil, err
	}
	b.WriteString("</plist>\n")
	return b.Bytes(), nil
}

func writeValue(b *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case dict:
		b.WriteString("<dict>")
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.WriteString("<key>")
			_ = xml.EscapeText(b, []byte(k))
			b.WriteString("</key>")
			if err := writeValue(b, v[k]); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		b.WriteString("</dict>\n")
	case ref:
		return writeValue(b, dict(v))
	case []any:
		b.WriteString("<array>")
		for _, item := range v {
			if err := writeValue(b, item); err != nil {
				return err
			}
		}
		b.WriteString("</array>\n")
	case string:
		b.WriteString("<string>")
		_ = xml.EscapeText(b, []byte(v))
		b.WriteString("</string>")
	case int:
		b.WriteString("<integer>" + strconv.Itoa(v) + "</integer>")
	case float64:
		b.WriteString("<real>" + strconv.FormatFloat(v, 'g', -1, 64) + "</real>")
	case bool:
		if v {
			b.WriteString("<true/>")
		} else {
			b.WriteString("<false/>")
		}
	default:
		return fmt.Errorf("plist 不支持类型 %T", v)
	}
	return nil
}
