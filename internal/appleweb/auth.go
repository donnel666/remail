package appleweb

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

const (
	UserAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	AutomatedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
	AcceptLanguage     = "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"
	Language           = "zh-CN"
	TimeZone           = "Asia/Shanghai"
	TimeZoneOffset     = "GMT+08:00"
	SecCHUA            = `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`
	automatedSecCHUA   = `"Not(A:Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"`
	SecCHPlatform      = `"macOS"`
	srpNLength         = 256
)

type BrowserProfile struct {
	UserAgent     string
	SecCHUA       string
	SecCHPlatform string
}

var automatedBrowserProfiles = [...]BrowserProfile{
	{
		UserAgent:     AutomatedUserAgent,
		SecCHUA:       automatedSecCHUA,
		SecCHPlatform: `"Windows"`,
	},
	// Keep resolving this identity for onboarding sessions created before
	// new automated sessions were pinned to Windows.
	{
		UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecCHUA:       SecCHUA,
		SecCHPlatform: `"Windows"`,
	},
	{
		UserAgent:     "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecCHUA:       SecCHUA,
		SecCHPlatform: `"Linux"`,
	},
}

// AutomatedBrowserProfile returns the fixed Windows identity used by new
// automated Apple sessions. The key remains for caller compatibility.
func AutomatedBrowserProfile(_ string) BrowserProfile {
	return automatedBrowserProfiles[0]
}

func BrowserProfileForUserAgent(userAgent string) (BrowserProfile, bool) {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == UserAgent {
		return BrowserProfile{UserAgent: UserAgent, SecCHUA: SecCHUA, SecCHPlatform: SecCHPlatform}, true
	}
	for _, profile := range automatedBrowserProfiles {
		if userAgent == profile.UserAgent {
			return profile, true
		}
	}
	return BrowserProfile{}, false
}

var (
	srpN = mustBigInt(
		"AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050" +
			"A37329CBB4A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50" +
			"E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B8" +
			"55F97993EC975EEAA80D740ADBF4FF747359D041D5C33EA71D281E446B14773B" +
			"CA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748" +
			"544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB3786160279004E57AE6" +
			"AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8E9DBFBB6" +
			"94B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F9E4AFF73",
	)
	srpG = big.NewInt(2)
)

func mustBigInt(value string) *big.Int {
	result, ok := new(big.Int).SetString(value, 16)
	if !ok {
		panic("invalid SRP group")
	}
	return result
}

func GroupN() *big.Int { return new(big.Int).Set(srpN) }

func GroupG() *big.Int { return new(big.Int).Set(srpG) }

func SRPPublicA() (*big.Int, []byte, error) {
	privateBytes := make([]byte, srpNLength)
	if _, err := rand.Read(privateBytes); err != nil {
		return nil, nil, fmt.Errorf("generate SRP private key: %w", err)
	}
	privateA := new(big.Int).SetBytes(privateBytes)
	publicA := new(big.Int).Exp(srpG, privateA, srpN)
	return privateA, padN(publicA.Bytes()), nil
}

func SRPProofs(identity, password string, salt []byte, iterations int, protocol string, privateA *big.Int, publicA, serverB []byte) ([]byte, []byte, error) {
	if privateA == nil || privateA.Sign() <= 0 || iterations < 1 {
		return nil, nil, fmt.Errorf("invalid SRP input")
	}
	passwordHash := sha256.Sum256([]byte(password))
	material := passwordHash[:]
	if protocol == "s2k_fo" {
		material = []byte(hex.EncodeToString(passwordHash[:]))
	}
	derived := pbkdf2.Key(material, salt, iterations, 32, sha256.New)
	publicAInt := new(big.Int).SetBytes(publicA)
	publicBInt := new(big.Int).SetBytes(serverB)
	if new(big.Int).Mod(new(big.Int).Set(publicBInt), srpN).Sign() == 0 {
		return nil, nil, fmt.Errorf("invalid SRP server key")
	}
	paddedA := padN(publicAInt.Bytes())
	paddedB := padN(publicBInt.Bytes())
	k := new(big.Int).SetBytes(hashParts(sha256.New, padN(srpN.Bytes()), padN(srpG.Bytes())))
	u := new(big.Int).SetBytes(hashParts(sha256.New, paddedA, paddedB))
	if u.Sign() == 0 {
		return nil, nil, fmt.Errorf("invalid SRP scrambling parameter")
	}
	inner := sha256.Sum256(append([]byte(":"), derived...))
	x := new(big.Int).SetBytes(hashParts(sha256.New, salt, inner[:]))
	gx := new(big.Int).Exp(srpG, x, srpN)
	base := new(big.Int).Sub(publicBInt, new(big.Int).Mul(k, gx))
	base.Mod(base, srpN)
	exponent := new(big.Int).Add(privateA, new(big.Int).Mul(u, x))
	secret := new(big.Int).Exp(base, exponent, srpN)
	sessionKey := hashParts(sha256.New, padN(secret.Bytes()))
	hx := xorHash(hashParts(sha256.New, padN(srpN.Bytes())), hashParts(sha256.New, padN(srpG.Bytes())))
	m1 := hashParts(sha256.New, hx, hashParts(sha256.New, []byte(identity)), salt, paddedA, serverB, sessionKey)
	m2 := hashParts(sha256.New, paddedA, m1, sessionKey)
	return m1, m2, nil
}

func padN(value []byte) []byte {
	if len(value) >= srpNLength {
		return append([]byte(nil), value[len(value)-srpNLength:]...)
	}
	result := make([]byte, srpNLength)
	copy(result[srpNLength-len(value):], value)
	return result
}

func PadN(value []byte) []byte { return padN(value) }

func hashParts(newHash func() hash.Hash, parts ...[]byte) []byte {
	digest := newHash()
	for _, part := range parts {
		_, _ = digest.Write(part)
	}
	return digest.Sum(nil)
}

func xorHash(left, right []byte) []byte {
	return new(big.Int).Xor(new(big.Int).SetBytes(left), new(big.Int).SetBytes(right)).Bytes()
}

func SolveHashcash(ctx context.Context, challenge string, bits int) (string, error) {
	if bits < 1 || bits > sha1.Size*8 {
		return "", fmt.Errorf("invalid Apple hashcash difficulty")
	}
	prefix := []byte(fmt.Sprintf("1:%d:%s:%s::", bits, time.Now().UTC().Format("20060102150405"), challenge))
	buffer := make([]byte, 0, len(prefix)+24)
	for counter := uint64(0); ; counter++ {
		buffer = append(buffer[:0], prefix...)
		buffer = strconv.AppendUint(buffer, counter, 10)
		digest := sha1.Sum(buffer)
		if hasLeadingZeroBits(digest[:], bits) {
			return string(buffer), nil
		}
		if counter&1023 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
	}
}

func hasLeadingZeroBits(value []byte, bits int) bool {
	fullBytes := bits / 8
	for index := 0; index < fullBytes; index++ {
		if value[index] != 0 {
			return false
		}
	}
	remaining := bits % 8
	return remaining == 0 || value[fullBytes]>>(8-remaining) == 0
}

func FrameID() (string, error) {
	parts := make([]string, 5)
	for index, size := range []int{8, 4, 4, 4, 10} {
		value, err := RandomString(size)
		if err != nil {
			return "", err
		}
		parts[index] = value
	}
	return "auth-" + strings.Join(parts, "-"), nil
}

func RandomString(size int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, size)
	random := make([]byte, size*2)
	written := 0
	for written < size {
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate random identifier: %w", err)
		}
		for _, value := range random {
			if value >= 252 {
				continue
			}
			result[written] = alphabet[int(value)%len(alphabet)]
			written++
			if written == size {
				break
			}
		}
	}
	return string(result), nil
}

type huffmanCode struct {
	size  int
	value int
}

var appleHuffmanTable = map[byte]huffmanCode{
	1: {4, 15}, 110: {8, 239}, 74: {8, 238}, 57: {7, 118}, 56: {7, 117},
	71: {8, 233}, 25: {8, 232}, 101: {5, 28}, 104: {7, 111}, 4: {7, 110},
	105: {6, 54}, 5: {7, 107}, 109: {7, 106}, 103: {9, 423}, 82: {9, 422},
	26: {8, 210}, 6: {7, 104}, 46: {6, 51}, 97: {6, 50}, 111: {6, 49},
	7: {7, 97}, 45: {7, 96}, 59: {5, 23}, 15: {7, 91}, 11: {8, 181},
	72: {8, 180}, 27: {8, 179}, 28: {8, 178}, 16: {7, 88}, 88: {10, 703},
	113: {11, 1405}, 89: {12, 2809}, 107: {13, 5617}, 90: {14, 11233},
	42: {15, 22465}, 64: {16, 44929}, 0: {16, 44928}, 81: {9, 350},
	29: {8, 174}, 118: {8, 173}, 30: {8, 172}, 98: {8, 171}, 12: {8, 170},
	99: {7, 84}, 117: {6, 41}, 112: {6, 40}, 102: {9, 319}, 68: {9, 318},
	31: {8, 158}, 100: {7, 78}, 84: {6, 38}, 55: {6, 37}, 17: {7, 73},
	8: {7, 72}, 9: {7, 71}, 77: {7, 70}, 18: {7, 69}, 65: {7, 68},
	48: {6, 33}, 116: {6, 32}, 10: {7, 63}, 121: {8, 125}, 78: {8, 124},
	80: {7, 61}, 69: {7, 60}, 119: {7, 59}, 13: {8, 117}, 79: {8, 116},
	19: {7, 57}, 67: {7, 56}, 114: {6, 27}, 83: {6, 26}, 115: {6, 25},
	14: {6, 24}, 122: {8, 95}, 95: {8, 94}, 76: {7, 46}, 24: {7, 45},
	37: {7, 44}, 50: {5, 10}, 51: {5, 9}, 108: {6, 17}, 22: {7, 33},
	120: {8, 65}, 66: {8, 64}, 21: {7, 31}, 106: {7, 30}, 47: {6, 14},
	53: {5, 6}, 49: {5, 5}, 86: {8, 39}, 85: {8, 38}, 23: {7, 18},
	75: {7, 17}, 20: {7, 16}, 2: {5, 3}, 73: {8, 23}, 43: {9, 45},
	87: {9, 44}, 70: {7, 10}, 3: {6, 4}, 52: {5, 1}, 54: {5, 0},
}

const appleHuffmanAlphabet = ".0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"

var appleCollectorReplacements = []string{
	"%20", ";;;", "%3B", "%2C", "und", "fin", "ed;", "%28", "%29", "%3A",
	"/53", "ike", "Web", "0;", ".0", "e;", "on", "il", "ck", "01", "in",
	"Mo", "fa", "00", "32", "la", ".1", "ri", "it", "%u", "le",
}

func FDClientInfo(now time.Time) (string, error) {
	return FDClientInfoFor(UserAgent, now)
}

func FDClientInfoFor(userAgent string, now time.Time) (string, error) {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = UserAgent
	}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(shanghai)
	current := fmt.Sprintf("%d/%d/%d %d:%d:%d", local.Year(), int(local.Month()), local.Day(), local.Hour(), local.Minute(), local.Second())
	slots := []string{"TF1", "020"}
	slots = append(slots, make([]string, 39)...)
	slots = append(slots, "false", "false", strconv.FormatInt(now.UnixMilli(), 10), "8", "2005/6/7%2021%3A33%3A44")
	slots = append(slots, make([]string, 8)...)
	slots = append(slots, "0", "-480", "-480", escapeCollectorValue(current))
	slots = append(slots, make([]string, 23)...)
	slots = append(slots, "25")
	slots = append(slots, make([]string, 14)...)
	slots = append(slots, "5.6.1-0", "")
	encoded, err := encodeAppleCollector(strings.Join(slots, ";") + ";")
	if err != nil {
		return "", err
	}
	payload := struct {
		UserAgent string `json:"U"`
		Language  string `json:"L"`
		Zone      string `json:"Z"`
		Version   string `json:"V"`
		Finger    string `json:"F"`
	}{userAgent, Language, TimeZoneOffset, "1.1", encoded}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func escapeCollectorValue(value string) string {
	var result strings.Builder
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("@*+-_./", rune(char)) {
			result.WriteByte(char)
			continue
		}
		fmt.Fprintf(&result, "%%%02X", char)
	}
	return result.String()
}

func encodeAppleCollector(raw string) (string, error) {
	replaced := raw
	for index, value := range appleCollectorReplacements {
		replaced = strings.ReplaceAll(replaced, value, string(rune(index+1)))
	}
	bits := make([]byte, 0, len(replaced)*8)
	put := func(size, value int) {
		for index := size - 1; index >= 0; index-- {
			bits = append(bits, byte((value>>index)&1))
		}
	}
	length := len(replaced)
	put(6, (7&length)<<3)
	put(6, (56&length)|1)
	for _, char := range []byte(replaced) {
		entry, ok := appleHuffmanTable[char]
		if !ok {
			return "", fmt.Errorf("apple fingerprint contains unsupported byte %d", char)
		}
		put(entry.size, entry.value)
	}
	terminator := appleHuffmanTable[0]
	put(terminator.size, terminator.value)
	for len(bits)%6 != 0 {
		bits = append(bits, 0)
	}
	encoded := make([]byte, 0, len(bits)/6+3)
	for offset := 0; offset < len(bits); offset += 6 {
		value := 0
		for _, bit := range bits[offset : offset+6] {
			value = value<<1 | int(bit)
		}
		encoded = append(encoded, appleHuffmanAlphabet[value])
	}
	crc := uint16(65535)
	for _, char := range []byte(replaced) {
		crc = ((crc >> 8) | (crc << 8)) ^ uint16(char)
		crc ^= (crc & 0xff) >> 4
		crc ^= crc << 12
		crc ^= (crc & 0xff) << 5
	}
	encoded = append(encoded, appleHuffmanAlphabet[crc>>12], appleHuffmanAlphabet[(crc>>6)&63], appleHuffmanAlphabet[crc&63])
	return string(encoded), nil
}

func EncodeCollector(raw string) (string, error) { return encodeAppleCollector(raw) }
