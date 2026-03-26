package csuite

import "fmt"

// ErrFormValidation is returned when form validation fails during submission.
var ErrFormValidation = fmt.Errorf("form validation failed: all fields are required")
