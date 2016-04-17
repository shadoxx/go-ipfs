package path

import (
	"errors"
	"path"
	"strings"

	key "github.com/ipfs/go-ipfs/blocks/key"

	b58 "gx/ipfs/QmT8rehPR3F6bmwL6zjUN8XpiDBFFpMP2myPdC6ApsWfJf/go-base58"
	mh "gx/ipfs/QmYf7ng2hG5XBtJA3tN34DQ2GUN5HNksEw1rLDkmr6vGku/go-multihash"
)

// ErrBadPath is returned when a given path is incorrectly formatted
var ErrBadPath = errors.New("invalid ipfs ref path")

// TODO: debate making this a private struct wrapped in a public interface
// would allow us to control creation, and cache segments.
type Path string

// FromString safely converts a string type to a Path type
func FromString(s string) Path {
	return Path(s)
}

// FromKey safely converts a Key type to a Path type
func FromKey(k key.Key) Path {
	return Path("/ipfs/" + k.String())
}

func (p Path) Segments() []string {
	cleaned := path.Clean(string(p))
	segments := strings.Split(cleaned, "/")

	// Ignore leading slash
	if len(segments[0]) == 0 {
		segments = segments[1:]
	}

	return segments
}

func (p Path) String() string {
	return string(p)
}

// IsJustAKey returns true if the path is of the form <key> or /ipfs/<key>.
func (p Path) IsJustAKey() bool {
	parts := p.Segments()
	return (len(parts) == 2 && parts[0] == "ipfs")
}

// PopLastSegment returns a new Path without its final segment, and the final
// segment, separately. If there is no more to pop (the path is just a key),
// the original path is returned.
func (p Path) PopLastSegment() (Path, string, error) {

	if p.IsJustAKey() {
		return p, "", nil
	}

	segs := p.Segments()
	newPath, err := ParsePath("/" + strings.Join(segs[:len(segs)-1], "/"))
	if err != nil {
		return "", "", err
	}

	return newPath, segs[len(segs)-1], nil
}

func FromSegments(prefix string, seg ...string) (Path, error) {
	return ParsePath(prefix + strings.Join(seg, "/"))
}

func ParsePath(txt string) (Path, error) {
	parts := strings.Split(txt, "/")
	if len(parts) == 1 {
		kp, err := ParseKeyToPath(txt)
		if err == nil {
			return kp, nil
		}
	}

	// if the path doesnt being with a '/'
	// we expect this to start with a hash, and be an 'ipfs' path
	if parts[0] != "" {
		if _, err := ParseKeyToPath(parts[0]); err != nil {
			return "", ErrBadPath
		}
		// The case when the path starts with hash without a protocol prefix
		return Path("/ipfs/" + txt), nil
	}

	if len(parts) < 3 {
		return "", ErrBadPath
	}

	if parts[1] == "ipfs" {
		if _, err := ParseKeyToPath(parts[2]); err != nil {
			return "", err
		}
	} else if parts[1] != "ipns" {
		return "", ErrBadPath
	}

	return Path(txt), nil
}

func ParseKeyToPath(txt string) (Path, error) {
	if txt == "" {
		return "", ErrNoComponents
	}

	chk := b58.Decode(txt)
	if len(chk) == 0 {
		return "", errors.New("not a key")
	}

	if _, err := mh.Cast(chk); err != nil {
		return "", err
	}
	return FromKey(key.Key(chk)), nil
}

func (p *Path) IsValid() error {
	_, err := ParsePath(p.String())
	return err
}

func Join(pths []string) string {
	return strings.Join(pths, "/")
}

func SplitList(pth string) []string {
	return strings.Split(pth, "/")
}
