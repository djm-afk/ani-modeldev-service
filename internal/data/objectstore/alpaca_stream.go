package objectstore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
)

// ConvertAlpacaJSON is the single import/prepare transformation. Import passes
// nil to retain only the digest; prepare supplies its unpublished private file.
// The only complete input allocation is one structurally bounded JSON object.
func ConvertAlpacaJSON(source io.Reader, destination io.Writer) (cpup01.PreparedDatasetFacts, error) {
	if source == nil {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	if destination == nil {
		destination = io.Discard
	}
	measured := &measuredReader{reader: source}
	reader := bufio.NewReaderSize(measured, 32<<10)
	if first, err := alpacaNonSpace(reader); err != nil || first != '[' {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	digest := sha256.New()
	writer := io.MultiWriter(destination, digest)
	facts := cpup01.PreparedDatasetFacts{Format: "ALPACA_JSONL", TransformID: "alpaca-array-compact-jsonl-v3"}
	for {
		first, err := alpacaNonSpace(reader)
		if err != nil || first != '{' {
			return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
		}
		record, err := alpacaRecord(reader)
		if err != nil {
			return cpup01.PreparedDatasetFacts{}, err
		}
		if err := strictAlpacaRecord(record); err != nil {
			return cpup01.PreparedDatasetFacts{}, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, record); err != nil {
			return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
		}
		compact.WriteByte('\n')
		size, err := cpup01.CheckedAdd(facts.SizeBytes, int64(compact.Len()))
		if err != nil {
			return cpup01.PreparedDatasetFacts{}, err
		}
		if n, err := writer.Write(compact.Bytes()); err != nil || n != compact.Len() {
			if err == nil {
				err = io.ErrShortWrite
			}
			return cpup01.PreparedDatasetFacts{}, err
		}
		facts.SizeBytes = size
		facts.RowCount, err = cpup01.CheckedAdd(facts.RowCount, 1)
		if err != nil {
			return cpup01.PreparedDatasetFacts{}, err
		}
		delimiter, err := alpacaNonSpace(reader)
		if err != nil || delimiter != ',' && delimiter != ']' {
			return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
		}
		if delimiter == ']' {
			break
		}
	}
	if _, err := alpacaNonSpace(reader); err != io.EOF || measured.readErr != nil || facts.SizeBytes > measured.size {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	facts.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return facts, nil
}

func alpacaNonSpace(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, err
		}
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return value, nil
		}
	}
}

func alpacaRecord(reader *bufio.Reader) ([]byte, error) {
	record := make([]byte, 1, 64<<10)
	record[0] = '{'
	quoted, escaped := false, false
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return nil, biz.ErrInputVerification
		}
		if len(record) == cpup01.MaxRecordBytes {
			return nil, &cpup01.LimitViolation{Reason: "DATASET_RECORD_TOO_LARGE", Kind: "dataset", LimitName: "MaxRecordBytes", Actual: int64(cpup01.MaxRecordBytes) + 1, Limit: cpup01.MaxRecordBytes}
		}
		record = append(record, value)
		if quoted {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				quoted = false
			}
			continue
		}
		switch value {
		case '"':
			quoted = true
		case '}':
			return record, nil
		case '{', '[':
			// Alpaca values are strings, so no nested object/array is valid.
			return nil, biz.ErrInputVerification
		}
	}
}

func strictAlpacaRecord(record []byte) error {
	if !utf8.Valid(record) {
		return biz.ErrInputVerification
	}
	decoder := json.NewDecoder(bytes.NewReader(record))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return biz.ErrInputVerification
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err := decoder.Token()
		key, valid := token.(string)
		if err != nil || !valid || seen[key] || key != "instruction" && key != "input" && key != "output" {
			return biz.ErrInputVerification
		}
		seen[key] = true
		token, err = decoder.Token()
		value, valid := token.(string)
		if err != nil || !valid || key != "input" && strings.TrimSpace(value) == "" {
			return biz.ErrInputVerification
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 3 {
		return biz.ErrInputVerification
	}
	if _, err := decoder.Token(); err != io.EOF {
		return biz.ErrInputVerification
	}
	return nil
}
