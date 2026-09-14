package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMaxBodySize(t *testing.T) {
	for _, value := range []string{"51m", "1024", "10K"} {
		if maxBodySize(value) != value {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"", "0", "1g", "51m; return 200;", "51m\n", "-1", "99999999999m"} {
		if maxBodySize(value) != "" {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"", "51m"} {
		var output bytes.Buffer
		if err := nginxConfTmpl.Execute(&output, confData{Name: "ocr", IP: "127.0.0.1", Port: "8000", Hostname: "ocr.example", MaxBodySize: value}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "client_max_body_size") != (value != "") {
			t.Fatal(output.String())
		}
	}
}
