package scene_audio_route_repository

import (
	"context"
	"fmt"
	"github.com/amitshekhariitbhu/go-backend-clean-architecture/domain"
	"github.com/amitshekhariitbhu/go-backend-clean-architecture/domain/domain_file_entity/scene_audio/scene_audio_route/scene_audio_route_interface"
	"github.com/amitshekhariitbhu/go-backend-clean-architecture/domain/domain_file_entity/scene_audio/scene_audio_route/scene_audio_route_models"
	"github.com/amitshekhariitbhu/go-backend-clean-architecture/mongo"
	"go.mongodb.org/mongo-driver/bson"
	"strconv"
	"strings"
	"time"
)

type albumRepository struct {
	db         mongo.Database
	collection string
}

func NewAlbumRepository(db mongo.Database, collection string) scene_audio_route_interface.AlbumRepository {
	return &albumRepository{
		db:         db,
		collection: collection,
	}
}

func (r *albumRepository) GetAlbumItems(
	ctx context.Context,
	start, end, sort, order, search, starred, artistId string,
	minYear, maxYear string,
) ([]scene_audio_route_models.AlbumMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	coll := r.db.Collection(r.collection)

	// 构建完整聚合管道
	pipeline := []bson.D{
		{
			{Key: "$lookup", Value: bson.D{
				{Key: "from", Value: domain.CollectionFileEntityAudioSceneAnnotation},
				{Key: "let", Value: bson.D{{Key: "albumId", Value: "$_id"}}},
				{Key: "pipeline", Value: []bson.D{
					{
						{Key: "$match", Value: bson.D{
							{Key: "$expr", Value: bson.D{
								{Key: "$and", Value: bson.A{
									bson.D{{Key: "$eq", Value: bson.A{"$item_id", "$$albumId"}}},
									bson.D{{Key: "$eq", Value: bson.A{"$item_type", "album"}}},
								}},
							}},
						}},
					},
				}},
				{Key: "as", Value: "annotations"},
			}},
		},
		{
			{Key: "$unwind", Value: bson.D{
				{Key: "path", Value: "$annotations"},
				{Key: "preserveNullAndEmptyArrays", Value: true},
			}},
		},
		{
			{Key: "$addFields", Value: bson.D{
				{Key: "play_count", Value: "$annotations.play_count"},
				{Key: "play_date", Value: "$annotations.play_date"},
				{Key: "rating", Value: "$annotations.rating"},
				{Key: "starred", Value: "$annotations.starred"},
				{Key: "starred_at", Value: "$annotations.starred_at"},
			}},
		},
	}

	// 核心逻辑：播放相关排序时过滤无效数据
	validatedSort := validateAlbumSortField(sort)
	if validatedSort == "play_count" || validatedSort == "play_date" {
		pipeline = append(pipeline, bson.D{
			{Key: "$match", Value: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: "play_count", Value: bson.D{{Key: "$gt", Value: 0}}}},
					bson.D{{Key: "play_date", Value: bson.D{{Key: "$ne", Value: nil}}}},
				}},
			}},
		})
	}

	// 其他过滤条件
	if match := buildAlbumMatch(search, starred, artistId, minYear, maxYear); len(match) > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$match", Value: match}})
	}

	// 排序处理 - 修复排序稳定性
	pipeline = append(pipeline, buildAlbumSortStage(validatedSort, order))

	// 分页处理 - 增强参数验证
	paginationStages := buildAlbumPaginationStage(start, end)
	if paginationStages != nil {
		pipeline = append(pipeline, paginationStages...)
	}

	// 执行查询
	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer cursor.Close(ctx)

	var results []scene_audio_route_models.AlbumMetadata
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	return results, nil
}

func (r *albumRepository) GetAlbumFilterItemsCount(
	ctx context.Context,
	search, starred, artistId, minYear, maxYear string,
) (*scene_audio_route_models.AlbumFilterCounts, error) {
	coll := r.db.Collection(r.collection)

	pipeline := []bson.D{
		{
			{Key: "$lookup", Value: bson.D{
				{Key: "from", Value: domain.CollectionFileEntityAudioSceneAnnotation},
				{Key: "let", Value: bson.D{{Key: "albumId", Value: "$_id"}}},
				{Key: "pipeline", Value: []bson.D{
					{
						{Key: "$match", Value: bson.D{
							{Key: "$expr", Value: bson.D{
								{Key: "$and", Value: bson.A{
									bson.D{{Key: "$eq", Value: bson.A{"$item_id", "$$albumId"}}},
									bson.D{{Key: "$eq", Value: bson.A{"$item_type", "album"}}},
								}},
							}},
						}},
					},
				}},
				{Key: "as", Value: "annotations"},
			}},
		},
		{
			{Key: "$match", Value: buildAlbumBaseMatch(search, starred, artistId, minYear, maxYear)},
		},
		{
			{Key: "$facet", Value: bson.D{
				{Key: "total", Value: []bson.D{
					{{Key: "$count", Value: "count"}},
				}},
				{Key: "starred", Value: []bson.D{
					{{Key: "$match", Value: bson.D{
						{Key: "annotations.starred", Value: true},
					}}},
					{{Key: "$count", Value: "count"}},
				}},
				{Key: "recent_play", Value: []bson.D{
					{{Key: "$match", Value: bson.D{
						{Key: "annotations.play_count", Value: bson.D{
							{Key: "$gt", Value: 0},
						}},
					}}},
					{{Key: "$count", Value: "count"}},
				}},
			}},
		},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("count query failed: %w", err)
	}
	defer func() {
		if closeErr := cursor.Close(ctx); closeErr != nil {
			fmt.Printf("cursor close error: %v\n", closeErr)
		}
	}()

	var result []struct {
		Total      []map[string]int `bson:"total"`
		Starred    []map[string]int `bson:"starred"`
		RecentPlay []map[string]int `bson:"recent_play"`
	}

	if err := cursor.All(ctx, &result); err != nil {
		return nil, fmt.Errorf("decode count error: %w", err)
	}

	counts := &scene_audio_route_models.AlbumFilterCounts{}
	if len(result) > 0 {
		counts.Total = extractCount(result[0].Total)
		counts.Starred = extractCount(result[0].Starred)
		counts.RecentPlay = extractCount(result[0].RecentPlay)
	}

	return counts, nil
}

// 优化过滤条件构建
func buildAlbumMatch(search, starred, artistId, minYear, maxYear string) bson.D {
	filter := bson.D{}

	// 优化艺术家过滤条件
	if artistId != "" {
		filter = append(filter, bson.E{
			Key: "$or",
			Value: bson.A{
				bson.D{{Key: "artist_id", Value: artistId}},
				bson.D{{Key: "all_artist_ids.artist_id", Value: artistId}},
			},
		})
	}

	// 增强年份过滤逻辑
	if minYear != "" || maxYear != "" {
		yearFilter := bson.D{}

		if minYear != "" {
			if year, err := strconv.Atoi(minYear); err == nil {
				yearFilter = append(yearFilter, bson.E{Key: "$gte", Value: year})
			}
		}

		if maxYear != "" {
			if year, err := strconv.Atoi(maxYear); err == nil {
				yearFilter = append(yearFilter, bson.E{Key: "$lte", Value: year})
			}
		}

		if len(yearFilter) > 0 {
			filter = append(filter, bson.E{
				Key: "$or",
				Value: bson.A{
					bson.D{{Key: "min_year", Value: yearFilter}},
					bson.D{{Key: "max_year", Value: yearFilter}},
				},
			})
		}
	}

	// 搜索条件
	if search != "" {
		filter = append(filter, bson.E{
			Key: "$or",
			Value: []bson.D{
				{{Key: "name", Value: bson.D{{Key: "$regex", Value: search}, {Key: "$options", Value: "i"}}}},
				{{Key: "artist", Value: bson.D{{Key: "$regex", Value: search}, {Key: "$options", Value: "i"}}}},
				{{Key: "album_artist", Value: bson.D{{Key: "$regex", Value: search}, {Key: "$options", Value: "i"}}}},
			},
		})
	}

	// Starred过滤
	if starred != "" {
		if isStarred, err := strconv.ParseBool(starred); err == nil {
			filter = append(filter, bson.E{Key: "starred", Value: isStarred})
		}
	}

	return filter
}

func buildAlbumBaseMatch(search, starred, artistId, minYear, maxYear string) bson.D {
	return buildAlbumMatch(search, starred, artistId, minYear, maxYear)
}

func validateAlbumSortField(sort string) string {
	sortMappings := map[string]string{
		"name":         "order_album_name",
		"artist":       "artist",
		"album_artist": "album_artist",
		"min_year":     "min_year",
		"max_year":     "max_year",
		"rating":       "rating",
		"starred_at":   "starred_at",
		"genre":        "genre",
		"song_count":   "song_count",
		"duration":     "duration",
		"size":         "size",
		"play_count":   "play_count",
		"play_date":    "play_date",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
	}

	lowerSort := strings.ToLower(sort)

	if mappedField, exists := sortMappings[lowerSort]; exists {
		return mappedField
	}

	validSortFields := map[string]bool{
		"order_album_name": true,
		"artist":           true,
		"album_artist":     true,
		"min_year":         true,
		"max_year":         true,
		"rating":           true,
		"starred_at":       true,
		"genre":            true,
		"song_count":       true,
		"duration":         true,
		"size":             true,
		"play_count":       true,
		"play_date":        true,
		"created_at":       true,
		"updated_at":       true,
	}

	if validSortFields[lowerSort] {
		return lowerSort
	}

	return "_id"
}

// 修复排序稳定性：添加唯一字段作为次要排序条件
func buildAlbumSortStage(sort, order string) bson.D {
	sortOrder := 1
	if order == "desc" {
		sortOrder = -1
	}
	return bson.D{
		{Key: "$sort", Value: bson.D{
			{Key: sort, Value: sortOrder},
			{Key: "_id", Value: 1}, // 关键修复：添加唯一字段保证排序稳定性
		}},
	}
}

// 增强分页参数验证
func buildAlbumPaginationStage(start, end string) []bson.D {
	startInt, err1 := strconv.Atoi(start)
	endInt, err2 := strconv.Atoi(end)

	// 参数验证
	if err1 != nil || err2 != nil || startInt < 0 || endInt <= startInt {
		return nil // 无效参数不添加分页阶段
	}

	skip := startInt
	limit := endInt - startInt

	var stages []bson.D
	if skip > 0 {
		stages = append(stages, bson.D{{Key: "$skip", Value: skip}})
	}
	if limit > 0 {
		stages = append(stages, bson.D{{Key: "$limit", Value: limit}})
	}

	return stages
}
