package app

import "github.com/ArronJablonowski/NexusRouter/attachments"

// Redact decoded text fields before JSON encoding. Raw string replacement can
// miss escaped credentials or corrupt image base64 and the attachment frame.
// Images remain binary inputs, not strings to rewrite or document authority.
func redactUploadContent(content string, secrets []string) (string, error) {
	text, files := attachments.Decode(content)
	if len(files) == 0 {
		return redact(content, secrets), nil
	}
	for i := range files {
		files[i].Name = redact(files[i].Name, secrets)
		files[i].Text = redact(files[i].Text, secrets)
	}
	clean := attachments.Encode(redact(text, secrets), files)
	if _, decoded := attachments.Decode(clean); len(decoded) != len(files) {
		return "", ErrAdmission
	}
	return clean, nil
}

// Browser/routing text views need names and extracted text, not image base64.
// Keeping binary data out also avoids false credential matches in text screens.
func uploadTextView(content string, secrets []string) (string, error) {
	clean, err := redactUploadContent(content, secrets)
	if err != nil {
		return "", err
	}
	text, files := attachments.Decode(clean)
	for i := range files {
		if files[i].Image != "" {
			files[i].Image = ""
			files[i].Text = "Image attached; original available in chat files."
		}
	}
	return attachments.Encode(text, files), nil
}
