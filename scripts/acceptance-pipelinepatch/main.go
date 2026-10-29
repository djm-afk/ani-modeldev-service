// Command acceptance-pipelinepatch repoints one material at a new pipeline
// version and re-canonicalizes it.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: acceptance-pipelinepatch <in> <out> <version-id> <ir-sha256>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic(err)
	}
	var implementation map[string]any
	if err := json.Unmarshal(fields["implementation"], &implementation); err != nil {
		panic(err)
	}
	implementation["pipeline_version_id"] = os.Args[3]
	implementation["pipeline_ir_sha256"] = os.Args[4]
	encoded, err := json.Marshal(implementation)
	if err != nil {
		panic(err)
	}
	fields["implementation"] = encoded
	joined, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	var material cpup01.ImplementationMaterial
	if err := json.Unmarshal(joined, &material); err != nil {
		panic(err)
	}
	canonical, err := material.Canonical()
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[2], canonical, 0o644); err != nil {
		panic(err)
	}
	fmt.Println("wrote", os.Args[2])
}
