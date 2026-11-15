package importer

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"time"
)

// receiptFormat is one provider's receipt export. Supporting another is a
// new entry in receiptFormats and a test with a made-up export in its layout.
type receiptFormat struct {
	ID, Name string
	Read     func(data []byte) ([]receipt, bool) // false when data is not this export
}

var receiptFormats = []receiptFormat{
	// Nexi Smart Receipt (formerly Storebox): the zip of a data export, with
	// storebox/receipts.json, or that file alone.
	{ID: "nexi", Name: "Nexi Smart Receipt", Read: readNexi},
}

// parseReceipts reads data in the receipt format it is in.
func parseReceipts(data []byte) (receiptFormat, []receipt, error) {
	for _, f := range receiptFormats {
		if receipts, ok := f.Read(data); ok {
			return f, receipts, nil
		}
	}
	return receiptFormat{}, nil, errNoReceipts
}

// zipFile is the file named name in the zip data, or nil when data is no such zip.
func zipFile(data []byte, name string) []byte {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxReceiptFile))
		if err != nil {
			return nil
		}
		return b
	}
	return nil
}

// readNexi reads a Nexi export. Its receipts carry receiptId, receiptLines and
// purchaseDateTimeString, which together mark the format.
func readNexi(data []byte) ([]receipt, bool) {
	if b := zipFile(data, "storebox/receipts.json"); b != nil {
		data = b
	}
	var raw []struct {
		ReceiptID string `json:"receiptId"`
		Date      string `json:"purchaseDateTimeString"`
		Merchant  struct {
			Name string `json:"name"`
		} `json:"merchant"`
		Price struct {
			Amount float64 `json:"amount"`
		} `json:"price"`
		Lines *[]struct {
			Name  string `json:"name"`
			Total struct {
				Amount float64 `json:"amount"`
			} `json:"totalPrice"`
		} `json:"receiptLines"`
	}
	if json.Unmarshal(data, &raw) != nil || len(raw) == 0 {
		return nil, false
	}
	var out []receipt
	for _, r := range raw {
		at, err := time.Parse(time.RFC3339, r.Date)
		if r.ReceiptID == "" || len(r.ReceiptID) > 64 || r.Lines == nil || err != nil {
			return nil, false
		}
		rc := receipt{ID: r.ReceiptID, Booked: time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC),
			Store: cut(r.Merchant.Name, maxBankText), Total: money(r.Price.Amount)}
		if rc.Store == "" {
			rc.Store = r.ReceiptID
		}
		for _, l := range *r.Lines {
			rc.Items = append(rc.Items, receiptItem{Name: cut(l.Name, 255), Amount: money(l.Total.Amount)})
		}
		out = append(out, rc)
	}
	return out, true
}
