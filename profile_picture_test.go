package songya

import "testing"

func TestProfilePictureMalformedFieldsDoNotHideRecords(t *testing.T) {
	for _, input := range []string{"profile_picture: garbage", "profile_picture: {filename: ../outside.png}", "profile_picture: {filename: ''}"} {
		doc := Parse([]byte("---\n" + input + "\n---\nIntact words."))
		if doc.ProfilePicture() != nil || doc.Body != "Intact words." {
			t.Fatal(input, doc.ProfilePicture(), doc.Body)
		}
	}
	doc := Parse([]byte("---\nprofile_picture: {filename: portrait.jpg, x: '0.8', y: -3, zoom: '2'}\n---\nWords"))
	p := doc.ProfilePicture()
	if p == nil || p.X != .8 || p.Y != 0 || p.Zoom != 2 {
		t.Fatal(p)
	}
	doc = Parse([]byte("---\nprofile_picture: {filename: portrait.jpg, x: .nan, zoom: invalid}\n---\nWords"))
	p = doc.ProfilePicture()
	if p == nil || p.X != .5 || p.Y != .5 || p.Zoom != 1 {
		t.Fatal(p)
	}
}

func TestProfilePictureCanReferenceAnotherRecord(t *testing.T) {
	doc := Parse([]byte("---\nprofile_picture: {record: m_123456789abc, filename: photo.HEIC, x: 0.3, y: 0.8, zoom: 2}\n---\nIdentity words."))
	p := doc.ProfilePicture()
	if p == nil || p.Record != "m_123456789abc" || p.Filename != "photo.HEIC" || p.X != .3 || p.Y != .8 || p.Zoom != 2 {
		t.Fatal(p)
	}
}
