// Package capture extracts response values into variables.
package capture

import (
	"fmt"
	"strconv"

	"github.com/tidwall/gjson"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Apply runs every capture against resp and returns the new variables.
func Apply(captures []request.Capture, resp *core.Response) (map[string]string, error) {
	out := make(map[string]string, len(captures))
	for _, c := range captures {
		val, err := extract(c, resp)
		if err != nil {
			return nil, err
		}
		out[c.Var] = val
	}
	return out, nil
}

func extract(c request.Capture, resp *core.Response) (string, error) {
	switch c.From {
	case "status":
		return strconv.Itoa(resp.Status), nil
	case "header":
		v := resp.HeaderGet(c.Path)
		if v == "" {
			return "", fmt.Errorf("capture %q: header %q not present", c.Var, c.Path)
		}
		return v, nil
	case "body":
		res := gjson.GetBytes(resp.Body, c.Path)
		if !res.Exists() {
			return "", fmt.Errorf("capture %q: path %q not found in body", c.Var, c.Path)
		}
		return res.String(), nil
	default:
		return "", fmt.Errorf("capture %q: unknown source %q", c.Var, c.From)
	}
}
