package cpup01_test

import (
	"bytes"
	"testing"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

func TestCurrentMaterialFreezesActualRegisteredImageIdentity(t *testing.T) {
	material := conformance.MaterialV21()
	material.Program.ImageID = "img_72a769640ebe4abb8b4c19e482d4a50e"
	material.Program.ImageVersion = 1
	material.Program.ImageScope = cpup01.ImageScopePlatform
	raw, err := material.Canonical()
	if err != nil {
		t.Fatal("registered asset rejected", err)
	}
	if bytes.Contains(raw, []byte("image_version_id")) {
		t.Fatal("retired UUID image contract serialized")
	}
	decoded, _, err := cpup01.ParseMaterial(raw)
	if err != nil || decoded.Program.ImageID != material.Program.ImageID || decoded.Program.ImageVersion != 1 || decoded.Program.ImageScope != cpup01.ImageScopePlatform {
		t.Fatal("registered image identity changed", err)
	}
	for _, version := range []int64{0, -1} {
		bad := material
		bad.Program.ImageVersion = version
		if _, err := bad.Canonical(); err == nil {
			t.Fatal("missing image version accepted")
		}
	}
	material.Program.ImageID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := material.Canonical(); err == nil {
		t.Fatal("retired image UUID admitted as current ImageID")
	}
}
