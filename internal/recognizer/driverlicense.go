package recognizer

// driverLicenseKeywords mark a 4+6 digit sequence as a driver's license.
var driverLicenseKeywords = []string{"водительское удостоверение", "водительские права", "права", "вуд", "удостоверение"}

// DriverLicenseRecognizer recognizes Russian driver's license series and
// number (4+6 digits).
type DriverLicenseRecognizer struct{}

// Type implements Recognizer.
func (DriverLicenseRecognizer) Type() Type { return DriverLicense }

// Find implements Recognizer. The 10-digit form is ambiguous on its own, so a
// context word is required.
func (DriverLicenseRecognizer) Find(text string) ([]Fragment, error) {
	return findContextSigned(text, "- ", 10, driverLicenseKeywords, DriverLicense)
}
