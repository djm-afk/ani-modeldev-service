package cpup01

import "regexp"

// These values match the current Resource ImageScope contract. The immutable
// digest identifies bytes; this tuple preserves the real registered asset and
// its version without inventing a second UUID registry identity.
type ImageScope string

const ImageScopePlatform ImageScope = "platform"
const ImageScopeTenant ImageScope = "tenant"

var registeredImageIDPattern = regexp.MustCompile(`^img_[0-9a-f]{32}$`)

func validRegisteredImage(id string, version int64, scope ImageScope) bool {
	return registeredImageIDPattern.MatchString(id) && version > 0 && (scope == ImageScopePlatform || scope == ImageScopeTenant)
}
