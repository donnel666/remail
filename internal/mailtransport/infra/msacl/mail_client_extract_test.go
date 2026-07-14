package msacl

import "testing"

func TestExtractCodeFromEmail(t *testing.T) {
	const msSender = "account-security-noreply@accountprotection.microsoft.com"
	// Real Microsoft OTP body: greets the recipient by address (whose local
	// part carries digits) then states the code.
	msBody := func(recipient, otp string) string {
		return "Hi " + recipient + ", We received your request for a single-use code to use with your Microsoft account. Your single-use code is: " + otp + " Only enter this code on an official website or app."
	}

	cases := []struct {
		name  string
		email EmailObj
		want  string
	}{
		{
			name: "wrong stored code from recipient digits is ignored for body code",
			email: EmailObj{
				Subject:          "Your single-use code",
				Preview:          msBody("ocom_2472aca1a08c@aishop6.com", "654505"),
				VerificationCode: "2472", // inbound pipeline mis-read recipient digits
				From:             msSender,
				To:               "ocom_2472aca1a08c@aishop6.com",
			},
			want: "654505",
		},
		{
			name: "six-digit run inside recipient does not shadow real code",
			email: EmailObj{
				Subject: "Your single-use code",
				Preview: msBody("ocom_179caa910621@aishop6.com", "334455"),
				From:    msSender,
				To:      "ocom_179caa910621@aishop6.com",
			},
			want: "334455",
		},
		{
			name: "correct stored code present in body is returned",
			email: EmailObj{
				Subject:          "Your single-use code",
				Preview:          msBody("ocom_deadbeef0001@aishop6.com", "778899"),
				VerificationCode: "778899",
				From:             msSender,
				To:               "ocom_deadbeef0001@aishop6.com",
			},
			want: "778899",
		},
		{
			name: "language without keyword falls back to isolated six digits for MS sender",
			email: EmailObj{
				Subject: "Microsoft",
				Preview: "Hi ocom_2472aca1a08c@aishop6.com, 445566  tuku kodua.",
				From:    msSender,
				To:      "ocom_2472aca1a08c@aishop6.com",
			},
			want: "445566",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractCodeFromEmail(tc.email); got != tc.want {
				t.Fatalf("extractCodeFromEmail = %q, want %q", got, tc.want)
			}
		})
	}
}
