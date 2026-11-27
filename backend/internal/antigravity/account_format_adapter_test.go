package antigravity

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildTestZipBytes(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		hdr := &zip.FileHeader{
			Name:   name,
			Method: zip.Deflate,
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip header %s: %v", name, err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestConvertCredentialsToAGY_AllTenFormats(t *testing.T) {
	futureMs := time.Now().Add(4 * time.Hour).UnixMilli()
	futureISO := time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339)

	// 1. agy_native
	t.Run("1_agy_native", func(t *testing.T) {
		raw := `{
			"type": "antigravity",
			"email": "Native.User@Example.com",
			"project_id": "agy-native-proj",
			"access_token": "ya29.a0AfH6SM_native_tok",
			"refresh_token": "1//0gNativeRefreshToken999",
			"expiry_date": ` + jsonNumberStr(futureMs) + `
		}`
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "agy-user.json", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 1 || res.Accounts[0].SourceFormat != SourceFormatAgyNative {
			t.Fatalf("unexpected result: %+v", res)
		}
		acc := res.Accounts[0]
		if acc.Email != "native.user@example.com" || acc.ProjectID != "agy-native-proj" || acc.Expired {
			t.Fatalf("unexpected normalized account: %+v", acc)
		}
		verifyAGYCLIFilesGenerated(t, acc, true)
	})

	// 2. gcloud_adc
	t.Run("2_gcloud_adc", func(t *testing.T) {
		raw := `{
			"type": "authorized_user",
			"client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
			"client_secret": "GOCSPX-adcSecretValue123",
			"refresh_token": "1//0gGcloudADCRefreshToken888",
			"quota_project_id": "gcp-adc-billing-01"
		}`
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "application_default_credentials.json", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 1 || res.Accounts[0].SourceFormat != SourceFormatGCloudADC {
			t.Fatalf("expected gcloud_adc, got: %+v", res)
		}
		acc := res.Accounts[0]
		if acc.ProjectID != "gcp-adc-billing-01" || !acc.HasClientSecret || !acc.HasRefreshToken {
			t.Fatalf("unexpected ADC fields: %+v", acc)
		}
		verifyAGYCLIFilesGenerated(t, acc, true)
	})

	// 3. gemini_cli (oauth_creds.json + companion google_accounts.json)
	t.Run("3_gemini_cli_with_companion_google_accounts", func(t *testing.T) {
		oauthCreds := `{
			"access_token": "ya29.a0GeminiCLIAccess",
			"refresh_token": "1//0gGeminiCLIRefresh777",
			"token_type": "Bearer",
			"expiry_date": ` + jsonNumberStr(futureMs) + `,
			"scope": "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email"
		}`
		googleAccounts := `{
			"active": "gemini-cli-owner@gmail.com",
			"old": ["legacy@gmail.com"]
		}`
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: ".gemini/oauth_creds.json", Content: []byte(oauthCreds)},
			{Filename: ".gemini/google_accounts.json", Content: []byte(googleAccounts)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 1 || res.InvalidCount != 0 {
			t.Fatalf("expected 1 paired valid account and 0 invalid, got summary=%+v", res.Summary)
		}
		acc := res.Accounts[0]
		if acc.SourceFormat != SourceFormatGeminiCLI || acc.Email != "gemini-cli-owner@gmail.com" {
			t.Fatalf("expected paired email from google_accounts.json, got %+v", acc)
		}
		verifyAGYCLIFilesGenerated(t, acc, true)
	})

	// 4. cpa_export (CLIProxyAPI / Sub2API / OneAPI wrapper + stringified credentials + nested token/oauth)
	t.Run("4_cpa_sub2api_oneapi_export", func(t *testing.T) {
		raw := `{
			"accounts": [
				{
					"name": "sub2api-node-a",
					"email": "cpa1@example.com",
					"credentials": "{\"refresh_token\":\"1//0gCPAStringifiedRT1\",\"access_token\":\"ya29.cpa1\",\"project_id\":\"cpa-proj-1\"}",
					"proxy_url": "http://u1:secretpass@10.0.0.8:8080"
				},
				{
					"email": "cpa2@example.com",
					"oauth": {
						"refresh_token": "1//0gCPANestedOAuthRT2",
						"expires_at": "` + futureISO + `"
					}
				}
			]
		}`
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "cpa-export.json", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 2 {
			t.Fatalf("expected 2 accounts from CPA export, got %+v", res)
		}
		if res.Accounts[0].SourceFormat != SourceFormatCPAExport || strings.Contains(res.Accounts[0].Proxy, "secretpass") {
			t.Fatalf("expected cpa_export and redacted proxy preview, got %+v", res.Accounts[0])
		}
		if res.Accounts[0].ProjectID != "cpa-proj-1" || res.Accounts[1].Email != "cpa2@example.com" {
			t.Fatalf("unexpected CPA accounts: %+v", res.Accounts)
		}
	})

	// 5. json_array (heterogeneous array)
	t.Run("5_json_array_heterogeneous", func(t *testing.T) {
		raw := `[
			{"type":"authorized_user","client_id":"cid-1.apps.googleusercontent.com","client_secret":"GOCSPX-1","refresh_token":"1//0gArrADC1","quota_project_id":"p-arr-1"},
			{"type":"antigravity","email":"arr2@example.com","refresh_token":"1//0gArrAGY2","access_token":"ya29.arr2"}
		]`
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "heterogeneous.json", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 2 || res.Accounts[0].SourceFormat != SourceFormatJSONArray || res.Accounts[0].InnerFormat != SourceFormatGCloudADC {
			t.Fatalf("unexpected json_array result: %+v", res.Accounts)
		}
	})

	// 6. ndjson / .jsonl
	t.Run("6_ndjson_jsonl", func(t *testing.T) {
		raw := "# comment line\n" +
			`{"email":"nd1@example.com","refresh_token":"1//0gNDJSONLine1","project_id":"nd-p1"}` + "\n" +
			`{"type":"authorized_user","refresh_token":"1//0gNDJSONLine2","client_secret":"GOCSPX-nd2"}` + "\n"
		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "accounts.jsonl", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 2 || res.Accounts[0].SourceFormat != SourceFormatNDJSON {
			t.Fatalf("expected 2 ndjson accounts, got %+v", res)
		}
	})

	// 7. csv_tsv (headered CSV + headerless CSV + TSV)
	t.Run("7_csv_and_tsv_headered_and_headerless", func(t *testing.T) {
		headeredCSV := "email,refresh_token,client_id,client_secret,project_id,proxy\n" +
			"csv1@example.com,1//0gCSVTok1,cid1.apps.googleusercontent.com,GOCSPX-csv1,proj-csv-1,http://127.0.0.1:7890\n"
		headerlessCSV := "csv2@example.com,1//0gCSVTok2,cid2.apps.googleusercontent.com,GOCSPX-csv2,proj-csv-2,socks5://127.0.0.1:1080\n"
		tsvText := "email\trefresh_token\tproject_id\ntsv1@example.com\t1//0gTSVTok3\tproj-tsv-3\n"

		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "headered.csv", Content: []byte(headeredCSV)},
			{Filename: "headerless.csv", Content: []byte(headerlessCSV)},
			{Filename: "accounts.tsv", Content: []byte(tsvText)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 3 {
			t.Fatalf("expected 3 CSV/TSV accounts, got %+v", res)
		}
		for _, a := range res.Accounts {
			if a.SourceFormat != SourceFormatCSVTSV {
				t.Fatalf("expected csv_tsv source format, got %s", a.SourceFormat)
			}
		}
		if res.Accounts[1].ProjectID != "proj-csv-2" || res.Accounts[2].ProjectID != "proj-tsv-3" {
			t.Fatalf("unexpected CSV/TSV project IDs: %+v", res.Accounts)
		}
	})

	// 8. delimited_text (email----password----refresh_token, email----refresh_token, email:refresh_token, label|refresh_token, bare 1//...)
	t.Run("8_delimited_text_all_variants", func(t *testing.T) {
		raw := strings.Join([]string{
			"# Mixed vendor text formats",
			"vendor1@example.com----MyAccountLoginPass!----1//0gDelimThreePartToken01",
			"vendor2@example.com----1//0gDelimTwoPartToken02",
			"vendor3@example.com:1//0gColonDelimToken03",
			"team-alpha-slot|1//0gPipeDelimToken04",
			"1//0gBareRefreshTokenLine05",
		}, "\n")

		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "tokens.txt", Content: []byte(raw)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 5 {
			t.Fatalf("expected 5 delimited_text accounts, got %d (%+v)", res.ValidCount, res.Accounts)
		}
		// 严格验证三段式 `email----password----refresh_token` 提取的是真实 1// RefreshToken 而非中间登录密码
		if res.Accounts[0].RefreshToken != "1//0gDelimThreePartToken01" || res.Accounts[0].Email != "vendor1@example.com" {
			t.Fatalf("password/token misparsed on 3-part line: email=%q rt=%q", res.Accounts[0].Email, res.Accounts[0].RefreshToken)
		}
		if res.Accounts[3].Name != "team-alpha-slot" || res.Accounts[3].RefreshToken != "1//0gPipeDelimToken04" {
			t.Fatalf("pipe label|token misparsed: %+v", res.Accounts[3])
		}
		if res.Accounts[4].RefreshToken != "1//0gBareRefreshTokenLine05" {
			t.Fatalf("bare 1// token misparsed: %+v", res.Accounts[4])
		}
	})

	// 9. base64_blob (whole JSON blob + base64 per line)
	t.Run("9_base64_blob_and_per_line", func(t *testing.T) {
		wholeJSON := `{"type":"antigravity","email":"b64whole@example.com","refresh_token":"1//0gBase64WholeTok1"}`
		wholeB64 := base64.StdEncoding.EncodeToString([]byte(wholeJSON))

		line1B64 := base64.StdEncoding.EncodeToString([]byte(`{"email":"b64line1@example.com","refresh_token":"1//0gBase64LineTok2"}`))
		line2B64 := base64.RawURLEncoding.EncodeToString([]byte(`b64line2@example.com----1//0gBase64LineTok3`))

		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "blob.txt", Content: []byte(wholeB64)},
			{Filename: "lines.b64", Content: []byte(line1B64 + "\n" + line2B64)},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ValidCount != 3 {
			t.Fatalf("expected 3 base64 accounts, got %d (%+v)", res.ValidCount, res.Accounts)
		}
		for _, a := range res.Accounts {
			if a.SourceFormat != SourceFormatBase64Blob {
				t.Fatalf("expected base64_blob format, got %s", a.SourceFormat)
			}
		}
	})

	// 10. zip_archive (multi-file + nested zip + companion .gemini/google_accounts.json)
	t.Run("10_zip_archive_recursive", func(t *testing.T) {
		innerZip := buildTestZipBytes(t, map[string][]byte{
			"nested/adc.json": []byte(`{"type":"authorized_user","refresh_token":"1//0gNestedZipADCTok","quota_project_id":"nested-proj"}`),
		})
		outerZip := buildTestZipBytes(t, map[string][]byte{
			"acc1/.gemini/oauth_creds.json":     []byte(`{"access_token":"ya29.z1","refresh_token":"1//0gZipGeminiTok1","token_type":"Bearer","expiry_date":1893456000000}`),
			"acc1/.gemini/google_accounts.json": []byte(`{"active":"zip-gemini@example.com","old":[]}`),
			"batch/lines.txt":                   []byte("zip-txt@example.com----1//0gZipTxtTok2\n"),
			"__MACOSX/._ignored.json":           []byte("garbage"),
			"subarchive.zip":                    innerZip,
		})

		res, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "accounts-pack.zip", Content: outerZip},
		}, ConvertOptions{})
		if err != nil {
			t.Fatalf("unexpected zip error: %v", err)
		}
		if res.ValidCount != 3 || res.InvalidCount != 0 {
			t.Fatalf("expected 3 valid accounts from zip and 0 invalid (__MACOSX skipped), got summary=%+v accounts=%+v", res.Summary, res.Accounts)
		}
		for _, a := range res.Accounts {
			if a.SourceFormat != SourceFormatZipArchive {
				t.Fatalf("expected zip_archive format, got %s", a.SourceFormat)
			}
		}
		if res.Accounts[0].Email != "zip-gemini@example.com" {
			t.Fatalf("expected .gemini companion email pairing inside zip, got %q", res.Accounts[0].Email)
		}
		if res.BundleZipBase64 == "" {
			t.Fatal("expected non-empty bundle_zip_base64")
		}
	})
}

func TestZipSlipSecurityGuardAndDeduplication(t *testing.T) {
	// 1. Zip-Slip 恶意路径穿越拦截
	maliciousPaths := []string{
		"../../etc/passwd",
		"..\\..\\Windows\\win.ini",
		"accounts/../../../escape.json",
		"/abs/path/auth.json",
		"C:/evil/auth.json",
	}
	for _, evil := range maliciousPaths {
		evilZip := buildTestZipBytes(t, map[string][]byte{
			evil: []byte(`{"refresh_token":"1//0gEvil"}`),
		})
		_, err := ConvertCredentialsToAGY([]AdapterInputFile{
			{Filename: "evil.zip", Content: evilZip},
		}, ConvertOptions{})
		if !errors.Is(err, ErrZipSlipDetected) {
			t.Fatalf("expected ErrZipSlipDetected for zip entry %q, got: %v", evil, err)
		}
	}

	// 2. 重复凭证自动去重与互补字段智能合并（第一条仅含裸 1// Token，第二条带邮箱与 project_id）
	raw1 := "1//0gSharedDupRefreshToken999\n"
	raw2 := `{"email":"merged@example.com","refresh_token":"1//0gSharedDupRefreshToken999","project_id":"merged-gcp-proj"}`
	res, err := ConvertCredentialsToAGY([]AdapterInputFile{
		{Filename: "bare.txt", Content: []byte(raw1)},
		{Filename: "rich.json", Content: []byte(raw2)},
	}, ConvertOptions{})
	if err != nil {
		t.Fatalf("dedup convert failed: %v", err)
	}
	if res.ValidCount != 1 || res.DuplicateCount != 1 || res.Total != 2 {
		t.Fatalf("expected ValidCount=1 DuplicateCount=1 Total=2, got summary=%+v", res.Summary)
	}
	merged := res.Accounts[0]
	if merged.Email != "merged@example.com" || merged.ProjectID != "merged-gcp-proj" {
		t.Fatalf("expected complementary email and project_id merged into primary account, got %+v", merged)
	}

	// 3. WriteIsolatedAGYAuthFiles & SaveLocalAuthFile 落盘真实 agy CLI 文件结构验证
	isoDir := filepath.Join(t.TempDir(), "account-1", "auths")
	written, err := WriteIsolatedAGYAuthFiles(isoDir, merged, merged.AuthFilename)
	if err != nil {
		t.Fatalf("WriteIsolatedAGYAuthFiles failed: %v", err)
	}
	if len(written) < 4 {
		t.Fatalf("expected >=4 files written (.gemini/oauth_creds.json, google_accounts.json, ADC, auth), got %v", written)
	}

	oauthRaw, err := os.ReadFile(filepath.Join(isoDir, ".gemini", "oauth_creds.json"))
	if err != nil {
		t.Fatalf("missing .gemini/oauth_creds.json: %v", err)
	}
	var oauthMap map[string]any
	if err := json.Unmarshal(oauthRaw, &oauthMap); err != nil {
		t.Fatalf("invalid oauth_creds.json: %v", err)
	}
	if oauthMap["refresh_token"] != "1//0gSharedDupRefreshToken999" || oauthMap["token_type"] != "Bearer" {
		t.Fatalf("unexpected oauth_creds.json content: %+v", oauthMap)
	}

	gaRaw, err := os.ReadFile(filepath.Join(isoDir, ".gemini", "google_accounts.json"))
	if err != nil {
		t.Fatalf("missing .gemini/google_accounts.json: %v", err)
	}
	if !strings.Contains(string(gaRaw), "merged@example.com") {
		t.Fatalf("google_accounts.json missing active email: %s", string(gaRaw))
	}

	// ListLocalAuthFiles 只返回顶层 1 份凭证文件，不会把 .gemini 伴生索引当成多账号或损坏文件
	listed, err := ListLocalAuthFiles(isoDir)
	if err != nil || len(listed) != 1 || listed[0].Email != "merged@example.com" {
		t.Fatalf("ListLocalAuthFiles unexpected: len=%d listed=%+v err=%v", len(listed), listed, err)
	}
}

func verifyAGYCLIFilesGenerated(t *testing.T, acc ConvertedAGYAccount, wantADC bool) {
	t.Helper()
	for _, mustKey := range []string{
		".gemini/oauth_creds.json",
		".gemini/google_accounts.json",
		"auth.json",
	} {
		content, ok := acc.AGYCLIFiles[mustKey]
		if !ok || strings.TrimSpace(content) == "" {
			t.Fatalf("missing generated agy CLI file %q in %+v", mustKey, acc.AGYCLIFiles)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			t.Fatalf("generated file %q is not valid JSON: %v", mustKey, err)
		}
	}
	if wantADC {
		if _, ok := acc.AGYCLIFiles[".config/gcloud/application_default_credentials.json"]; !ok {
			t.Fatalf("expected .config/gcloud/application_default_credentials.json to be generated")
		}
	}
}

func jsonNumberStr(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
