package onec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docflow/internal/config"
	"docflow/internal/domain"

	"github.com/google/uuid"
)

func typedTestExporter(
	t *testing.T,
) (*fileExporter, string) {
	t.Helper()

	dir := t.TempDir()
	exp, err := newFileExporter(config.OneCConfig{
		FileDir:       dir,
		ExportProfile: config.OneCExportProfileTypeInbox,
	})
	if err != nil {
		t.Fatal(err)
	}
	return exp, dir
}

func typedTestPayload(docType string) Payload {
	return Payload{
		DocumentID: uuid.NewString(),
		Kind:       "create",
		DocType:    docType,
		TypeInbox:  true,
		Number:     "123",
		Fields:     map[string]string{},
	}
}

func TestTypeInboxWritesFiveFolders(t *testing.T) {
	exp, dir := typedTestExporter(t)

	types := []string{
		"act",
		"waybill",
		"contract",
		"power_of_attorney",
		"upd",
	}

	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			p := typedTestPayload(typ)

			// Company routing must not override business type.
			p.CompanyFolder = "company_123"

			if err := exp.Export(
				context.Background(), p,
			); err != nil {
				t.Fatal(err)
			}

			file := filepath.Join(
				dir, typ,
				p.DocumentID+"_create.xml",
			)
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(
				string(content),
				"<Type>"+typ+"</Type>",
			) {
				t.Fatal("incorrect XML document type")
			}
			if !strings.Contains(
				string(content),
				"<RecognitionQuality",
			) {
				t.Fatal("missing quality diagnostics")
			}

			entries, err := os.ReadDir(
				filepath.Join(dir, typ),
			)
			if err != nil || len(entries) != 1 {
				t.Fatalf(
					"unexpected files: %v, %v",
					entries, err,
				)
			}
		})
	}

	if _, err := os.Stat(
		filepath.Join(dir, "company_123"),
	); !os.IsNotExist(err) {
		t.Fatal("company folder was created in typed profile")
	}
}

func TestTypeInboxRepeatedExportDoesNotOverwrite(t *testing.T) {
	exp, dir := typedTestExporter(t)
	p := typedTestPayload("act")

	if err := exp.Export(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := exp.Export(context.Background(), p); err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}

	path := filepath.Join(
		dir, "act", p.DocumentID+"_create.xml",
	)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	p.Number = "DIFFERENT"
	if err := exp.Export(
		context.Background(), p,
	); err == nil {
		t.Fatal("conflicting XML was overwritten")
	}

	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("existing XML content changed")
	}

	entries, err := os.ReadDir(filepath.Join(dir, "act"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected temporary files: %v %v",
			entries, err)
	}
}

func TestTypeInboxRejectsInvalidPayloads(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*Payload)
	}{
		{"unknown type", func(p *Payload) {
			p.DocType = "unknown"
		}},
		{"unsupported type", func(p *Payload) {
			p.DocType = "unsupported_document"
		}},
		{"path traversal", func(p *Payload) {
			p.DocType = "../contract"
		}},
		{"uncanonical alias", func(p *Payload) {
			p.DocType = "invoice"
		}},
		{"missing typed marker", func(p *Payload) {
			p.TypeInbox = false
		}},
		{"bad document ID", func(p *Payload) {
			p.DocumentID = "../escape"
		}},
		{"missing document ID", func(p *Payload) {
			p.DocumentID = ""
		}},
		{"bad export kind", func(p *Payload) {
			p.Kind = "../create"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp, dir := typedTestExporter(t)
			p := typedTestPayload("act")
			tc.modify(&p)

			if err := exp.Export(
				context.Background(), p,
			); err == nil {
				t.Fatal("invalid payload was accepted")
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("invalid payload created files or folders")
			}
		})
	}
}

func TestTypeInboxRejectsSymlinkFolder(t *testing.T) {
	exp, dir := typedTestExporter(t)
	outside := t.TempDir()

	if err := os.Symlink(
		outside, filepath.Join(dir, "act"),
	); err != nil {
		t.Fatal(err)
	}

	p := typedTestPayload("act")
	if err := exp.Export(
		context.Background(), p,
	); err == nil {
		t.Fatal("symlink directory was accepted")
	}

	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("writer escaped the outbox root")
	}
}

func TestTypeInboxNormalizesAliasesViaBuilder(
	t *testing.T,
) {
	exp, dir := typedTestExporter(t)
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType: "invoice",
		},
	}

	p, err := BuildTypeInboxPayload(
		doc, domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if p.DocType != "act" {
		t.Fatalf("alias normalized to %q", p.DocType)
	}

	if err := exp.Export(
		context.Background(), p,
	); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		dir, "act", p.DocumentID+"_create.xml",
	)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("alias did not reach act folder:", err)
	}
}
