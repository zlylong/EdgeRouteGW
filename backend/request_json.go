package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"
)

func decodeStrictJSON(c *gin.Context, dst interface{}, allowEmptyBody bool) error {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if allowEmptyBody && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected extra JSON content")
		}
		return err
	}
	return nil
}
