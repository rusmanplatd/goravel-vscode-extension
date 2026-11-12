package controllers

import "github.com/rusmanplatd/basegoravel/app/facades"

type CityController struct{}

func NewCityController() *CityController {
	return &CityController{}
}

func (c *CityController) Index() string {
	return facades.Config().GetString("jwt.secret")
}

