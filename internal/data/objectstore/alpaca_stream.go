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
//
// Two source containers are accepted and both produce the identical derived
// JSONL of one compact object per line:
//   - a JSON array of records (the original form);
//   - one JSON object per line (JSONL), which is how the public Alpaca corpora
//     are actually published.
//
// Record field names accept the `_zh` aliases the Chinese Alpaca corpora use and
// are normalised to the canonical English keys in the derived bytes, so the
// training loader sees exactly one shape regardless of the source.
func ConvertAlpacaJSON(source io.Reader, destination io.Writer) (cpup01.PreparedDatasetFacts, error) {
	if source == nil {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	if destination == nil {
		destination = io.Discard
	}
	measured := &measuredReader{reader: source}
	reader := bufio.NewReaderSize(measured, 32<<10)
	first, err := alpacaNonSpace(reader)
	if err != nil {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	digest := sha256.New()
	writer := io.MultiWriter(destination, digest)
	facts := cpup01.PreparedDatasetFacts{Format: "ALPACA_JSONL", TransformID: "alpaca-array-compact-jsonl-v3"}

	switch first {
	case '[':
		// Records are separated by commas; the closing bracket ends the source.
		for {
			if err := alpacaAppendRecord(reader, writer, &facts); err != nil {
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
	case '{':
		// JSONL: the first object was consumed for detection, so the record
		// reader starts from it and every following line is another object.
		if err := alpacaAppendRecordFrom(reader, writer, &facts, '{'); err != nil {
			return cpup01.PreparedDatasetFacts{}, err
		}
		for {
			next, err := alpacaNonSpace(reader)
			if err == io.EOF {
				break
			}
			if err != nil || next != '{' {
				return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
			}
			if err := alpacaAppendRecordFrom(reader, writer, &facts, '{'); err != nil {
				return cpup01.PreparedDatasetFacts{}, err
			}
		}
	default:
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}

	if _, err := alpacaNonSpace(reader); err != io.EOF || measured.readErr != nil || facts.SizeBytes > measured.size {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	if facts.RowCount == 0 {
		return cpup01.PreparedDatasetFacts{}, biz.ErrInputVerification
	}
	facts.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return facts, nil
}

// alpacaAppendRecord reads the next record from an array source.
func alpacaAppendRecord(reader *bufio.Reader, writer io.Writer, facts *cpup01.PreparedDatasetFacts) error {
	first, err := alpacaNonSpace(reader)
	if err != nil || first != '{' {
		return biz.ErrInputVerification
	}
	return alpacaAppendRecordFrom(reader, writer, facts, '{')
}

// alpacaAppendRecordFrom validates one object, normalises its keys and writes
// the compact line. `opened` is the brace already consumed by the caller.
func alpacaAppendRecordFrom(reader *bufio.Reader, writer io.Writer, facts *cpup01.PreparedDatasetFacts, opened byte) error {
	record, err := alpacaRecord(reader)
	if err != nil {
		return err
	}
	normalised, err := normaliseAlpacaRecord(record)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, normalised); err != nil {
		return biz.ErrInputVerification
	}
	compact.WriteByte('\n')
	size, err := cpup01.CheckedAdd(facts.SizeBytes, int64(compact.Len()))
	if err != nil {
		return err
	}
	if n, err := writer.Write(compact.Bytes()); err != nil || n != compact.Len() {
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	facts.SizeBytes = size
	facts.RowCount, err = cpup01.CheckedAdd(facts.RowCount, 1)
	return err
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

// alpacaFieldAliases maps the source spellings this feature accepts onto the
// canonical keys. The Chinese Alpaca corpora use the `_zh` forms; the derived
// record always carries the English keys so the training loader has one shape.
var alpacaFieldAliases = map[string]string{
	"instruction":    "instruction",
	"input":          "input",
	"output":         "output",
	"instruction_zh": "instruction",
	"input_zh":       "input",
	"output_zh":      "output",
}

// normaliseAlpacaRecord validates one record and rewrites its keys to the
// canonical names.
//
// The public Chinese Alpaca corpora put both the English and the `_zh` value of
// each field in the same object. That is not ambiguous: the English field is the
// canonical one and the `_zh` field is its translation, which this feature does
// not consume. So an English field wins and `_zh` fills in only when the English
// spelling is absent.
//
// A record that carries only the three canonical keys is returned untouched, so
// existing sources keep byte-identical derived output. Only a record that used an
// alias is re-serialised, in the fixed canonical order with no HTML escaping.
func normaliseAlpacaRecord(record []byte) ([]byte, error) {
	if !utf8.Valid(record) {
		return nil, biz.ErrInputVerification
	}
	decoder := json.NewDecoder(bytes.NewReader(record))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, biz.ErrInputVerification
	}
	type slot struct {
		value   string
		present bool
		english bool
	}
	values := map[string]*slot{"instruction": {}, "input": {}, "output": {}}
	seenKeys := make(map[string]bool, 6)
	aliased := false
	for decoder.More() {
		token, err := decoder.Token()
		key, valid := token.(string)
		if err != nil || !valid || seenKeys[key] {
			return nil, biz.ErrInputVerification
		}
		seenKeys[key] = true
		canonical, known := alpacaFieldAliases[key]
		if !known {
			return nil, biz.ErrInputVerification
		}
		isEnglish := canonical == key
		if !isEnglish {
			aliased = true
		}
		token, err = decoder.Token()
		value, valid := token.(string)
		if err != nil || !valid || canonical != "input" && strings.TrimSpace(value) == "" {
			return nil, biz.ErrInputVerification
		}
		current := values[canonical]
		// An English value always wins; a second English spelling of the same
		// field is a duplicate, which the key set above already rejects.
		if !current.present || isEnglish && !current.english {
			current.value, current.present, current.english = value, true, isEnglish
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, biz.ErrInputVerification
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, biz.ErrInputVerification
	}
	for _, canonical := range []string{"instruction", "input", "output"} {
		if !values[canonical].present {
			return nil, biz.ErrInputVerification
		}
	}
	if !aliased {
		return record, nil
	}
	return canonicalAlpacaRecord(map[string]string{
		"instruction": values["instruction"].value,
		"input":       values["input"].value,
		"output":      values["output"].value,
	})
}

// canonicalAlpacaRecord renders the three fields in a fixed order without HTML
// escaping, so the same records always produce the same bytes.
func canonicalAlpacaRecord(values map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range []string{"instruction", "input", "output"} {
		if index > 0 {
			buffer.WriteByte(',')
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(values[key]); err != nil {
			return nil, biz.ErrInputVerification
		}
		buffer.WriteString(`"` + key + `":`)
		buffer.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}
