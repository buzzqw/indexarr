package downloadclient

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexInt and flexFloat keep the queue readable when a client changes a field
// type between versions. qBittorrent 5.x and some Deluge plugins send numbers
// as strings ("progress": "0.53"); with a plain int64/float64 one such value
// fails the whole payload and the queue looks empty.

// flexInt accepts a JSON number, a number sent as a string, null or "".
type flexInt int64

func (f *flexInt) UnmarshalJSON(raw []byte) error {
	value, err := flexNumber(raw)
	if err != nil {
		return err
	}
	*f = flexInt(value)
	return nil
}

// flexFloat accepts a JSON number, a number sent as a string, null or "".
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(raw []byte) error {
	value, err := flexNumber(raw)
	if err != nil {
		return err
	}
	*f = flexFloat(value)
	return nil
}

func flexNumber(raw []byte) (float64, error) {
	text := strings.TrimSpace(string(raw))
	switch text {
	case "", "null":
		return 0, nil
	}
	if strings.HasPrefix(text, `"`) {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, err
		}
		text = strings.TrimSpace(decoded)
		if text == "" {
			return 0, nil
		}
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return float64(value), nil
	}
	return strconv.ParseFloat(text, 64)
}
