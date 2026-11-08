package loop

// opinionLimit is the largest review opinion stored on a review_verdict event.
const opinionLimit = 64 * 1024

// truncateOpinion keeps the opinion text and reports whether it was cut.
// The stored card ReworkNote is not passed through here.
func truncateOpinion(s string) (string, bool) {
	if len(s) <= opinionLimit {
		return s, false
	}
	return s[:opinionLimit], true
}
