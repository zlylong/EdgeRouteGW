package main

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

func main() {
	repo := NewAppRepository()
	service := NewAppService(repo)
	controller := NewAppController()

	service.Bootstrap()
	r := controller.BuildRouter()
	controller.Run(r)
}
