package document

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestExtractTextIsDeterministicCandidate(t *testing.T) {
	content := []byte("# Architecture\n\nThe framework is implemented in Go.\n")
	first, err := ExtractBytes("guide.md", content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractBytes("guide.md", content)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceSHA256 != second.SourceSHA256 || first.Chunks[0].ID != second.Chunks[0].ID {
		t.Fatalf("non-deterministic extraction: %#v %#v", first, second)
	}
	if first.State != "CANDIDATE" || first.AutoApprovalEligible || first.Engine != "go-stdlib-text-v1" {
		t.Fatalf("result = %#v", first)
	}
}

func TestExtractDOCXWithPureGoBackend(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	contentTypes, _ := archive.Create("[Content_Types].xml")
	_, _ = contentTypes.Write([]byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`))
	rels, _ := archive.Create("_rels/.rels")
	_, _ = rels.Write([]byte(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`))
	document, _ := archive.Create("word/document.xml")
	_, _ = document.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Go document extraction works.</w:t></w:r></w:p></w:body></w:document>`))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractBytes("report.docx", buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chunks) == 0 || result.Text == "" || result.Engine != "tabula-go-v1.6.14" {
		t.Fatalf("result = %#v", result)
	}
}

func TestImageProducesAuditableMetadataWithoutFabricatedOCR(t *testing.T) {
	value := image.NewRGBA(image.Rect(0, 0, 12, 8))
	value.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, value); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractBytes("diagram.png", buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["width"] != 12 || result.Metadata["height"] != 8 || len(result.Chunks) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSecretContentFailsClosed(t *testing.T) {
	if _, err := ExtractBytes("secret.md", []byte("authorization: bearer should-not-be-ingested")); err == nil {
		t.Fatal("secret content was accepted")
	}
}
