package handler

// ResponseHelper 响应辅助结构体
type ResponseHelper struct{}

// NewResponseHelper 创建响应辅助实例
func NewResponseHelper() *ResponseHelper {
	return &ResponseHelper{}
}

// Success 创建成功响应
func (r *ResponseHelper) Success(data any, message string) ApiResponse {
	return ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	}
}

// Error 创建错误响应
func (r *ResponseHelper) Error(errorCode int, message string) ApiResponse {
	return ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	}
}
