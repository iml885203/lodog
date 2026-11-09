package redact

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const sentinel = "SENTINEL-8f3a"

func TestQueryMasksOnlyTheNamedParameters(t *testing.T) {
	got := Query(url.Values{"api_key": {sentinel}, "page": {"2"}}, []string{"api_key"})
	if got.Get("api_key") != Mask || got.Get("page") != "2" {
		t.Errorf("got %v", got)
	}
}

func TestTextMasksEveryFormACredentialTakesInAMessage(t *testing.T) {
	n := Names{Queries: []string{"api_key", "access token"}, Headers: []string{"X-Api-Key"}}
	for name, in := range map[string]string{
		"url error":       `Get "https://h/api/x?page=2&api_key=` + sentinel + `&z=1": dial tcp: refused`,
		"first param":     `https://h/api/x?api_key=` + sentinel,
		"encoded name":    `https://h/x?access+token=` + sentinel,
		"escaped equals":  `https://h/x?api_key%3D` + sentinel,
		"owned header":    "X-Api-Key: " + sentinel,
		"bearer":          "Authorization: Bearer " + sentinel,
		"cookie":          "cookie: session=" + sentinel,
		"user info":       "https://bot:" + sentinel + "@h/x",
		"json query":      `{"api_key":"` + sentinel + `","page":2}`,
		"json spaced":     `{"api_key" : "` + sentinel + `"}`,
		"json header":     `{"Authorization":"Bearer ` + sentinel + `"}`,
		"json own hdr":    `{"X-Api-Key":"` + sentinel + `"}`,
		"encoded 5F":      `https://h/x?api%5Fkey=` + sentinel,
		"spaces around =": "api_key = " + sentinel,
		"fragment":        `https://h/x#api_key=` + sentinel,
		"after fragment":  `https://h/x?page=1#frag&api_key=` + sentinel,
	} {
		if out := n.Text(in); strings.Contains(out, sentinel) {
			t.Errorf("%s: still has the secret: %s", name, out)
		}
	}
}

func TestTextKeepsWhatIsNotACredential(t *testing.T) {
	n := Names{Queries: []string{"api_key"}}
	in := `Get "https://h/api/x?page=2&sort=name": dial tcp 127.0.0.1:1: connection refused`
	if out := n.Text(in); out != in {
		t.Errorf("changed an innocent message: %s", out)
	}
}

func TestTheHeaderRoundTrips(t *testing.T) {
	names := []string{"api_key", "token"}
	got := ParseHeader(FormatHeader(names))
	if len(got) != 2 || got[0] != "api_key" || got[1] != "token" {
		t.Errorf("got %v", got)
	}
	if len(ParseHeader(" , ")) != 0 {
		t.Error("blank names must be dropped")
	}
}

func TestHeldValuesAreMaskedInEveryFormTheyTravelIn(t *testing.T) {
	held := `p@ss "w/rd"&1`
	n := Names{Values: []string{held}}
	enc, _ := json.Marshal(held)
	for name, in := range map[string]string{
		"raw":   "bad " + held + " here",
		"query": "?k=" + url.QueryEscape(held),
		"path":  "/x/" + url.PathEscape(held),
		"json":  `{"v":` + string(enc) + `}`,
		// Some encoders escape the slash too.
		"json slash": `{"v":"` + strings.ReplaceAll(strings.Trim(string(enc), `"`), "/", `\/`) + `"}`,
	} {
		for _, out := range []string{n.Value(in), n.Text(in)} {
			if strings.Contains(out, "w/rd") || strings.Contains(out, "w%2Frd") || strings.Contains(out, `w\/rd`) || strings.Contains(out, `\"w/rd`) {
				t.Errorf("%s: %s", name, out)
			}
		}
	}
}

// A value match touches only the secret itself, so it is safe on a 2xx body.
func TestValueMaskingLeavesBusinessDataAlone(t *testing.T) {
	n := Names{Queries: []string{"token"}, Values: []string{sentinel}}
	in := `{"token":"a-public-field","data":[1,2]}`
	if out := n.Value(in); out != in {
		t.Errorf("changed data: %s", out)
	}
}

// A short secret would ruin real data, so a response is not scanned for it;
// an error still is.
func TestAShortSecretIsNotMaskedInDataButIsInErrors(t *testing.T) {
	n := Names{Values: []string{"1234", "a"}}
	data := `{"id":1234,"status":"active"}`
	if out := n.Value(data); out != data {
		t.Errorf("data was changed: %s", out)
	}
	if out := n.Text("rejected 1234"); strings.Contains(out, "1234") {
		t.Errorf("error leaks a short secret: %s", out)
	}
	if n.Short() != 2 {
		t.Errorf("Short = %d, want 2", n.Short())
	}
	long := Names{Values: []string{"exactly8"}}
	if out := long.Value("x exactly8 y"); strings.Contains(out, "exactly8") {
		t.Errorf("a secret at the minimum length must be masked: %s", out)
	}
}

func TestPreviewMasksCredentialNamedFieldsAtAnyDepthAndClipsLongStrings(t *testing.T) {
	long := strings.Repeat("A", 5000)
	got := PreviewJSON(map[string]any{
		"user": "bob", "Password": "pw1", "nested": map[string]any{"api_key": "k1", "list": []any{map[string]any{"accessToken": "t1"}}},
		"blob": long,
	}, false)
	for _, leaked := range []string{"pw1", "k1", "t1", long[:100]} {
		if strings.Contains(got, leaked) {
			t.Errorf("preview shows %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "bob") || !strings.Contains(got, "5000 字元") {
		t.Errorf("preview lost the ordinary value or the size: %s", got)
	}
}

func TestPreviewJSONIsCappedWhateverTheShape(t *testing.T) {
	many := map[string]any{}
	for i := 0; i < 500; i++ {
		many[strings.Repeat("k", 10)+string(rune('a'+i%26))+strings.Repeat("x", i)] = "v"
	}
	if got := PreviewJSON(many, true); len([]rune(got)) > MaxPreviewRunes+40 {
		t.Errorf("preview is %d characters", len(got))
	}
}

func TestFieldListNamesFieldsAndNeverValues(t *testing.T) {
	got := FieldList(map[string]any{"password": "hunter2", "amount": 1})
	if got != "body fields: amount, password" || FieldList(nil) != "" {
		t.Errorf("FieldList = %q", got)
	}
}

func TestEveryCredentialSpellingIsRecognisedAsAFieldName(t *testing.T) {
	for _, name := range []string{
		"password", "Password", "passwd", "passphrase", "pwd", "PWD", "userPassword", "new_password", "confirm-password",
		"secret", "clientSecret", "client_secret", "SECRET_KEY",
		"token", "accessToken", "access_token", "access-token", "refreshToken", "x-auth-token", "csrfToken",
		"jwt", "idJwt", "JWT",
		"bearer", "Bearer",
		"session", "sessionId", "session_id", "JSESSIONID",
		"cookie", "Set-Cookie",
		"api_key", "apiKey", "api-key", "ApiKey", "X-Api-Key",
		"access_key", "accessKey", "access-key", "AccessKey", "AWS_ACCESS_KEY_ID",
		"private_key", "privateKey", "private-key",
		"credential", "credentials", "Authorization", "proxy-authorization",
		"pin", "userPin", "user_pin", "otp", "OTP", "cvv", "cvc", "pass",
	} {
		if !IsCredentialName(name) {
			t.Errorf("%q is a credential name but is not recognised", name)
		}
	}
}

func TestOrdinaryFieldNamesAreNotTakenForCredentials(t *testing.T) {
	for _, name := range []string{
		"refNo", "amount", "username", "spinner", "footprint", "pinned", "compass", "passenger",
		"opinion", "typing", "keyword", "key", "note",
	} {
		if IsCredentialName(name) {
			t.Errorf("%q is an ordinary name but is masked", name)
		}
	}
}

func TestPreviewMasksACredentialInsideAStringValue(t *testing.T) {
	for name, in := range map[string]string{
		"user info":    "https://bot:" + sentinel + "@host.example/x",
		"token":        "https://h/x?page=2&token=" + sentinel,
		"api_key":      "https://h/x?api_key=" + sentinel + "&page=2",
		"access_token": "https://h/x?access_token=" + sentinel,
		"password":     "https://h/x?password=" + sentinel,
		"secret":       "https://h/x?secret=" + sentinel,
		"camel":        "https://h/x?apiKey=" + sentinel,
	} {
		for where, v := range map[string]any{"top": in, "nested": map[string]any{"a": []any{in}}} {
			got, _ := json.Marshal(Preview(v))
			if strings.Contains(string(got), sentinel) {
				t.Errorf("%s (%s): %s", name, where, got)
			}
		}
	}
	if got := Preview("https://h/x?page=2&q=abc").(string); got != "https://h/x?page=2&q=abc" {
		t.Errorf("an ordinary URL changed: %s", got)
	}
}

func TestMaskStringMasksACredentialNameThatEndsALongerIdentifier(t *testing.T) {
	cases := []string{
		`https://shop.example/?resetPassword=test-reset-token-0000000000000000`,
		`{"userPassword":"hunter2hunter2"}`,
		`user_password: hunter2hunter2`,
		`clientSecret=hunter2hunter2&x=1`,
	}
	for _, in := range cases {
		out := MaskString(in)
		if strings.Contains(out, "hunter2") || strings.Contains(out, "test-reset-token") || !strings.Contains(out, Mask) {
			t.Errorf("MaskString(%q) = %q", in, out)
		}
	}
	if got := MaskString("page=2&sort=name"); got != "page=2&sort=name" {
		t.Errorf("a plain query changed: %q", got)
	}
}

func TestJSONLinesMasksACredentialAnywhereInAStreamAndKeepsTheRest(t *testing.T) {
	const link = "https://shop.example?resetPassword=test-reset-token-0000000000000000"
	in := `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"HTTP 200\n{\"data\":{\"resetUrl\":\"` + link + `\"}}"}]}]},"n":12345678901234567890}` + "\n" +
		`not json: Authorization: Bearer test-bearer-00000000` + "\n\n" +
		`{"type":"result","result":"2 deposits <ok> & 3 withdrawals"}` + "\n"
	out := string(JSONLines([]byte(in)))
	for _, leak := range []string{"test-reset-token", "test-bearer-00000000"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked:\n%s", leak, out)
		}
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Errorf("the lines changed:\n%s", out)
	}
	for _, kept := range []string{`12345678901234567890`, `2 deposits <ok> & 3 withdrawals`, `resetUrl`} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was lost:\n%s", kept, out)
		}
	}
}
