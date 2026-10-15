package documentcore

import (
	"strings"
	"testing"
)

func TestValidateSnapshotRenderOptionsAcceptsBoundedProfileInputs(t *testing.T) {
	inputs := map[string]string{"leafrun-data": "/input.json"}
	for index := 0; index < projectProfileInputCountMax; index++ {
		key := "option" + strings.Repeat("x", index/10) + string(rune('a'+index%10))
		inputs[key] = strings.Repeat("v", 1000)
	}
	gotInputs, standard, err := validateSnapshotRenderOptions(RenderSnapshotOptions{Inputs: inputs, PDFStandard: "1.7,a-2b"})
	if err != nil {
		t.Fatalf("valid bounded render options rejected: %v", err)
	}
	if len(gotInputs) != projectProfileInputCountMax+1 || standard != "1.7,a-2b" {
		t.Fatalf("validated inputs/profile = %d, %q", len(gotInputs), standard)
	}
	gotInputs["optiona"] = "changed"
	if inputs["optiona"] == "changed" {
		t.Fatal("validated render options alias caller input map")
	}
}

func TestValidateSnapshotRenderOptionsRejectsReservedAndUnsupportedValues(t *testing.T) {
	for _, options := range []RenderSnapshotOptions{
		{Inputs: map[string]string{"leafrun-data": "C:\\secret.json"}},
		{Inputs: map[string]string{"bad key": "value"}},
		{PDFStandard: "1.8"},
		{PDFStandard: "1.7,1.7"},
	} {
		if _, _, err := validateSnapshotRenderOptions(options); err == nil {
			t.Errorf("accepted invalid options: %+v", options)
		}
	}
}
