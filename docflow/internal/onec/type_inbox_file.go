package onec

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"docflow/internal/recognize"

	"github.com/google/uuid"
)

// exportTypeInbox publishes complete XML into a business-type folder.
// Existing files are never overwritten. The legacy file writer is
// deliberately not used by this function.
func (e *fileExporter) exportTypeInbox(p Payload) error {
	if !p.TypeInbox {
		return fmt.Errorf(
			"type_inbox: payload was not prepared by typed builder",
		)
	}

	businessType, allowed := recognize.TypeInboxDocType(
		p.DocType,
	)
	if !allowed || businessType != p.DocType {
		return fmt.Errorf(
			"type_inbox: unsupported or noncanonical type %q",
			p.DocType,
		)
	}

	switch p.Kind {
	case "create", "partial", "update":
	default:
		return fmt.Errorf(
			"type_inbox: unsupported export kind %q",
			p.Kind,
		)
	}

	id, err := uuid.Parse(p.DocumentID)
	if err != nil || id == uuid.Nil ||
		id.String() != p.DocumentID {
		return fmt.Errorf(
			"type_inbox: invalid document ID %q",
			p.DocumentID,
		)
	}

	body, err := xml.MarshalIndent(
		toEnterpriseData(p), "", "  ",
	)
	if err != nil {
		return fmt.Errorf(
			"type_inbox: marshal XML: %w", err,
		)
	}
	body = append([]byte(xml.Header), body...)

	// The directory name comes exclusively from the type allowlist.
	// CompanyFolder is intentionally ignored in this profile.
	dir := filepath.Join(e.dir, businessType)

	if err := os.Mkdir(dir, 0o750); err != nil &&
		!errors.Is(err, os.ErrExist) {
		return fmt.Errorf(
			"type_inbox: create directory: %w", err,
		)
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"type_inbox: %q is not a real directory", dir,
		)
	}

	name := fmt.Sprintf(
		"%s_%s.xml", p.DocumentID, p.Kind,
	)
	final := filepath.Join(dir, name)

	// Prepare the file under a temporary hidden name.
	// The receiver must not consume this temporary file.
	file, err := os.CreateTemp(
		dir, ".docflow-*.tmp",
	)
	if err != nil {
		return err
	}

	temporary := file.Name()
	defer os.Remove(temporary)

	if err := file.Chmod(0o640); err != nil {
		file.Close()
		return err
	}

	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	// Hard-link publication is atomic and refuses to replace
	// an existing destination, including under concurrent writers.
	err = os.Link(temporary, final)
	if err == nil {
		return nil
	}

	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf(
			"type_inbox: atomic publish: %w", err,
		)
	}

	// Retrying identical content is successful and idempotent.
	// Different content with the same identity is a conflict,
	// never a reason to overwrite an existing XML.
	existingInfo, err := os.Lstat(final)
	if err != nil {
		return err
	}
	if !existingInfo.Mode().IsRegular() {
		return fmt.Errorf(
			"type_inbox: existing destination is not a regular file",
		)
	}

	existing, err := os.ReadFile(final)
	if err != nil {
		return err
	}

	if bytes.Equal(existing, body) {
		return nil
	}

	return fmt.Errorf(
		"type_inbox: XML conflict for %s: refusing overwrite",
		name,
	)
}
