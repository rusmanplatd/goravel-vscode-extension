package routes

import (
	"github.com/rusmanplatd/basegoravel/app/facades"
	"github.com/rusmanplatd/basegoravel/app/http/controllers"
)

func Api() {
	cityController := controllers.NewCityController()
	facades.Route().Prefix("/api/v1").Get("/geo/cities", cityController.Index)
}

