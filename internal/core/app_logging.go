package liqmap

func SetupLogging(debug bool) (func(), error) {
	return setupLogging(debug)
}
