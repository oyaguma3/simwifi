package nai

import (
	"errors"
	"testing"
)

func TestGenerate(t *testing.T) {
	tests := []struct {
		name       string
		method     Method
		imsi       string
		operatorID string
		realm      string
		want       string
		wantErr    error
	}{
		{
			name:       "AKA 2桁MNC",
			method:     AKA,
			imsi:       "440103123456789",
			operatorID: "44010",
			want:       "0440103123456789@wlan.mnc010.mcc440.3gppnetwork.org",
		},
		{
			name:       "AKA' 2桁MNC",
			method:     AKAPrime,
			imsi:       "440103123456789",
			operatorID: "44010",
			want:       "6440103123456789@wlan.mnc010.mcc440.3gppnetwork.org",
		},
		{
			name:       "3桁MNC",
			method:     AKA,
			imsi:       "310410123456789",
			operatorID: "310410",
			want:       "0310410123456789@wlan.mnc410.mcc310.3gppnetwork.org",
		},
		{
			name:       "MNC 1桁目がゼロの3桁",
			method:     AKA,
			imsi:       "001001123456789",
			operatorID: "001001",
			want:       "0001001123456789@wlan.mnc001.mcc001.3gppnetwork.org",
		},
		{
			name:       "realm上書き",
			method:     AKAPrime,
			imsi:       "440103123456789",
			operatorID: "",
			realm:      "example.org",
			want:       "6440103123456789@example.org",
		},
		{
			name:       "realm上書き時はoperatorIDを無視",
			method:     AKA,
			imsi:       "440103123456789",
			operatorID: "99999",
			realm:      "wlan.mnc010.mcc440.3gppnetwork.org",
			want:       "0440103123456789@wlan.mnc010.mcc440.3gppnetwork.org",
		},
		{
			name:       "operatorIDが空",
			method:     AKA,
			imsi:       "440103123456789",
			operatorID: "",
			wantErr:    ErrNoOperatorID,
		},
		{
			name:       "operatorIDの長さ不正",
			method:     AKA,
			imsi:       "440103123456789",
			operatorID: "4401",
			wantErr:    ErrNoOperatorID,
		},
		{
			name:       "operatorIDがIMSIと不一致",
			method:     AKA,
			imsi:       "440103123456789",
			operatorID: "44020",
			wantErr:    ErrNoOperatorID,
		},
		{
			name:       "IMSIに数字以外",
			method:     AKA,
			imsi:       "44010312345678x",
			operatorID: "44010",
			wantErr:    ErrInvalidIMSI,
		},
		{
			name:       "IMSIが長すぎる",
			method:     AKA,
			imsi:       "4401031234567890",
			operatorID: "44010",
			wantErr:    ErrInvalidIMSI,
		},
		{
			name:       "IMSIが空",
			method:     AKA,
			imsi:       "",
			operatorID: "44010",
			wantErr:    ErrInvalidIMSI,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(tt.method, tt.imsi, tt.operatorID, tt.realm)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateInvalidRealm(t *testing.T) {
	for _, realm := range []string{"a@b", "a b"} {
		if _, err := Generate(AKA, "440103123456789", "", realm); err == nil {
			t.Errorf("realm %q: expected error", realm)
		}
	}
}

func TestParseMethod(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    Method
		eapName string
	}{
		{"aka", AKA, "AKA"},
		{"akap", AKAPrime, "AKA'"},
	} {
		m, err := ParseMethod(tt.in)
		if err != nil || m != tt.want || m.EAPName() != tt.eapName || m.String() != tt.in {
			t.Errorf("ParseMethod(%q) = %v, %v", tt.in, m, err)
		}
	}
	if _, err := ParseMethod("sim"); err == nil {
		t.Error("expected error for sim")
	}
}
