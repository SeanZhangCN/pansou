package model

import "time"

// DoubanRankingType 豆瓣榜单类型
type DoubanRankingType string

const (
	DoubanRankingMovieHot    DoubanRankingType = "movie_hot"
	DoubanRankingTVHot       DoubanRankingType = "tv_hot"
	DoubanRankingTVShow      DoubanRankingType = "tv_variety_show"
	DoubanRankingNewMovies   DoubanRankingType = "new_movies"
	DoubanRankingTVAnimation DoubanRankingType = "tv_animation"
	DoubanRankingTVAmerican  DoubanRankingType = "tv_american"
	DoubanRankingTVKorean    DoubanRankingType = "tv_korean"
	DoubanRankingTVJapanese  DoubanRankingType = "tv_japanese"
)

// DoubanRankingRequest 豆瓣榜单请求参数
type DoubanRankingRequest struct {
	Type  DoubanRankingType `form:"type" json:"type"`
	Limit int               `form:"limit" json:"limit"`
}

// DoubanRankingItem 豆瓣榜单条目（一期基础字段）
type DoubanRankingItem struct {
	SubjectID string  `json:"subject_id"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Rank      int     `json:"rank"`
	Score     float64 `json:"score"`
}

// DoubanRankingResponse 豆瓣榜单响应数据
type DoubanRankingResponse struct {
	Type      DoubanRankingType   `json:"type"`
	Name      string              `json:"name"`
	Total     int                 `json:"total"`
	UpdatedAt time.Time           `json:"updated_at"`
	Items     []DoubanRankingItem `json:"items"`
}
