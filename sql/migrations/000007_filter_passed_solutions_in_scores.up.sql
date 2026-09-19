-- Migration 007: Filter best-score and leaderboard queries to only consider
-- solutions with result=1 (pass). Failed and abandoned runs no longer count.

CREATE OR REPLACE FUNCTION get_best_scores(
    p_user_id UUID,
    p_levels JSON,
    p_scope VARCHAR(16)
) RETURNS TABLE (
    level_id VARCHAR(16),
    level_version INTEGER,
    score_type VARCHAR(16),
    best_score INTEGER,
    user_id UUID,
    display_name VARCHAR(64),
    friend_code VARCHAR(9)
) AS $$
DECLARE
    v_level_record JSON;
    v_level_id VARCHAR(16);
    v_level_version INTEGER;
    v_sql TEXT;
    v_where_conditions TEXT[];
    v_final_where TEXT;
BEGIN
    IF p_levels IS NULL THEN
        -- No levels specified: match all levels at their latest version.
        v_final_where := 's.level_version = (SELECT MAX(s2.level_version) FROM solution s2 WHERE s2.level_id = s.level_id)';
    ELSE
        FOR v_level_record IN SELECT * FROM json_array_elements(p_levels)
        LOOP
            v_level_id := v_level_record->>'level_id';
            v_level_version := (v_level_record->>'level_version')::INTEGER;

            IF v_level_version = -1 THEN
                v_where_conditions := array_append(v_where_conditions,
                    format('(s.level_id = %L AND s.level_version = (
                        SELECT MAX(s2.level_version)
                        FROM solution s2
                        JOIN score sc2 ON s2.id = sc2.solution_id
                        WHERE s2.level_id = %L
                    ))', v_level_id, v_level_id));
            ELSE
                v_where_conditions := array_append(v_where_conditions,
                    format('(s.level_id = %L AND s.level_version = %s)', v_level_id, v_level_version));
            END IF;
        END LOOP;

        IF array_length(v_where_conditions, 1) = 0 THEN
            RAISE EXCEPTION 'No valid level specifications provided';
        END IF;

        v_final_where := array_to_string(v_where_conditions, ' OR ');
    END IF;

    CASE p_scope
        WHEN 'personal' THEN
            v_sql := format('
                SELECT
                    s.level_id,
                    s.level_version,
                    sc.type_id as score_type,
                    CASE
                        WHEN st.higher_is_better THEN MAX(sc.score)
                        ELSE MIN(sc.score)
                    END as best_score,
                    u.id as user_id,
                    COALESCE(u.display_name, '''') as display_name,
                    u.friend_code
                FROM solution s
                JOIN score sc ON s.id = sc.solution_id
                JOIN score_type st ON sc.type_id = st.id
                JOIN "user" u ON s.user_id = u.id
                WHERE s.result = 1 AND s.user_id = %L AND (%s)
                GROUP BY s.level_id, s.level_version, sc.type_id, st.higher_is_better, u.id, u.display_name, u.friend_code
                ORDER BY s.level_id, s.level_version, sc.type_id
            ', p_user_id, v_final_where);

        WHEN 'friends' THEN
            RAISE EXCEPTION 'Friends scope not yet implemented';

        WHEN 'regional' THEN
            RAISE EXCEPTION 'Regional scope not yet implemented';

        WHEN 'global' THEN
            v_sql := format('
                WITH ranked_scores AS (
                    SELECT
                        s.level_id,
                        s.level_version,
                        sc.type_id as score_type,
                        sc.score as best_score,
                        u.id as user_id,
                        COALESCE(u.display_name, '''') as display_name,
                        u.friend_code,
                        ROW_NUMBER() OVER (
                            PARTITION BY s.level_id, s.level_version, sc.type_id
                            ORDER BY
                                CASE WHEN st.higher_is_better THEN sc.score END DESC,
                                CASE WHEN NOT st.higher_is_better THEN sc.score END ASC,
                                s.date_time_utc ASC,
                                s.id ASC
                        ) as score_rank
                    FROM solution s
                    JOIN score sc ON s.id = sc.solution_id
                    JOIN score_type st ON sc.type_id = st.id
                    JOIN "user" u ON s.user_id = u.id
                    WHERE s.result = 1 AND (%s)
                )
                SELECT
                    rs.level_id,
                    rs.level_version,
                    rs.score_type,
                    rs.best_score,
                    rs.user_id,
                    rs.display_name,
                    rs.friend_code
                FROM ranked_scores rs
                WHERE rs.score_rank = 1
                ORDER BY rs.level_id, rs.level_version, rs.score_type
            ', v_final_where);

        ELSE
            RAISE EXCEPTION 'Invalid scope: %', p_scope;
    END CASE;

    RETURN QUERY EXECUTE v_sql;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION get_leaderboard_count(
    p_user_id UUID,
    p_levels JSON,
    p_scope VARCHAR(16),
    p_score_type VARCHAR(16)
)
RETURNS INTEGER AS $$
DECLARE
    v_level_json JSON;
    v_where_conditions TEXT[] := '{}';
    v_final_where TEXT;
    v_sql TEXT;
    v_count INTEGER;
BEGIN
    FOR v_level_json IN SELECT * FROM json_array_elements(p_levels)
    LOOP
        IF (v_level_json->>'level_version')::INTEGER = -1 THEN
            v_where_conditions := array_append(v_where_conditions, format(
                '(s.level_id = %L AND s.level_version = (SELECT MAX(level_version) FROM solution WHERE level_id = %L))',
                v_level_json->>'level_id', v_level_json->>'level_id'
            ));
        ELSE
            v_where_conditions := array_append(v_where_conditions, format(
                '(s.level_id = %L AND s.level_version = %s)',
                v_level_json->>'level_id', v_level_json->>'level_version'
            ));
        END IF;
    END LOOP;

    v_final_where := 's.result = 1 AND (' || array_to_string(v_where_conditions, ' OR ') || ')';

    IF p_score_type IS NOT NULL AND p_score_type != '' THEN
        v_final_where := v_final_where || format(' AND sc.type_id = %L', p_score_type);
    END IF;

    CASE p_scope
        WHEN 'personal' THEN
            v_sql := format('
                SELECT COUNT(DISTINCT (s.level_id, s.level_version, sc.type_id))
                FROM solution s
                JOIN score sc ON s.id = sc.solution_id
                JOIN score_type st ON sc.type_id = st.id
                WHERE s.user_id = %L AND (%s)
            ', p_user_id, v_final_where);

        WHEN 'global' THEN
            v_sql := format('
                WITH user_best_scores AS (
                    SELECT
                        s.user_id,
                        s.level_id,
                        s.level_version,
                        sc.type_id,
                        CASE
                            WHEN st.higher_is_better THEN MAX(sc.score)
                            ELSE MIN(sc.score)
                        END as best_score_value
                    FROM solution s
                    JOIN score sc ON s.id = sc.solution_id
                    JOIN score_type st ON sc.type_id = st.id
                    WHERE (%s)
                    GROUP BY s.user_id, s.level_id, s.level_version, sc.type_id, st.higher_is_better
                )
                SELECT COUNT(*)
                FROM user_best_scores
            ', v_final_where);

        WHEN 'friends' THEN
            RAISE EXCEPTION 'Friends scope not yet implemented';

        WHEN 'regional' THEN
            RAISE EXCEPTION 'Regional scope not yet implemented';

        ELSE
            RAISE EXCEPTION 'Invalid scope: %', p_scope;
    END CASE;

    EXECUTE v_sql INTO v_count;
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION get_leaderboard(
    p_user_id UUID,
    p_levels JSON,
    p_scope VARCHAR(16),
    p_score_type VARCHAR(16),
    p_offset INTEGER,
    p_size INTEGER
)
RETURNS TABLE(
    rank INTEGER,
    level_id VARCHAR(16),
    level_version INTEGER,
    score_type VARCHAR(16),
    best_score INTEGER,
    user_id UUID,
    display_name VARCHAR(64),
    friend_code VARCHAR(9)
) AS $$
DECLARE
    v_level_json JSON;
    v_where_conditions TEXT[] := '{}';
    v_final_where TEXT;
    v_sql TEXT;
BEGIN
    FOR v_level_json IN SELECT * FROM json_array_elements(p_levels)
    LOOP
        IF (v_level_json->>'level_version')::INTEGER = -1 THEN
            v_where_conditions := array_append(v_where_conditions, format(
                '(s.level_id = %L AND s.level_version = (SELECT MAX(level_version) FROM solution WHERE level_id = %L))',
                v_level_json->>'level_id', v_level_json->>'level_id'
            ));
        ELSE
            v_where_conditions := array_append(v_where_conditions, format(
                '(s.level_id = %L AND s.level_version = %s)',
                v_level_json->>'level_id', v_level_json->>'level_version'
            ));
        END IF;
    END LOOP;

    v_final_where := 's.result = 1 AND (' || array_to_string(v_where_conditions, ' OR ') || ')';

    IF p_score_type IS NOT NULL AND p_score_type != '' THEN
        v_final_where := v_final_where || format(' AND sc.type_id = %L', p_score_type);
    END IF;

    CASE p_scope
        WHEN 'personal' THEN
            v_sql := format('
                SELECT
                    ROW_NUMBER() OVER (
                        ORDER BY s.level_id, s.level_version, sc.type_id,
                        CASE WHEN st.higher_is_better THEN sc.score END DESC,
                        CASE WHEN NOT st.higher_is_better THEN sc.score END ASC,
                        s.date_time_utc ASC
                    )::INTEGER as rank,
                    s.level_id,
                    s.level_version,
                    sc.type_id as score_type,
                    sc.score as best_score,
                    u.id as user_id,
                    COALESCE(u.display_name, '''') as display_name,
                    u.friend_code
                FROM solution s
                JOIN score sc ON s.id = sc.solution_id
                JOIN score_type st ON sc.type_id = st.id
                JOIN "user" u ON s.user_id = u.id
                WHERE s.user_id = %L AND (%s)
                ORDER BY rank
                OFFSET %s LIMIT %s
            ', p_user_id, v_final_where, p_offset, p_size);

        WHEN 'global' THEN
            v_sql := format('
                WITH user_best_scores AS (
                    SELECT
                        s.user_id,
                        s.level_id,
                        s.level_version,
                        sc.type_id,
                        CASE
                            WHEN st.higher_is_better THEN MAX(sc.score)
                            ELSE MIN(sc.score)
                        END as best_score_value,
                        MIN(s.date_time_utc) as earliest_date
                    FROM solution s
                    JOIN score sc ON s.id = sc.solution_id
                    JOIN score_type st ON sc.type_id = st.id
                    WHERE (%s)
                    GROUP BY s.user_id, s.level_id, s.level_version, sc.type_id, st.higher_is_better
                ),
                ranked_scores AS (
                    SELECT
                        ROW_NUMBER() OVER (
                            PARTITION BY ubs.level_id, ubs.level_version, ubs.type_id
                            ORDER BY
                                CASE WHEN st.higher_is_better THEN ubs.best_score_value END DESC,
                                CASE WHEN NOT st.higher_is_better THEN ubs.best_score_value END ASC,
                                ubs.earliest_date ASC
                        )::INTEGER as level_rank,
                        ROW_NUMBER() OVER (
                            ORDER BY ubs.level_id, ubs.level_version, ubs.type_id,
                                CASE WHEN st.higher_is_better THEN ubs.best_score_value END DESC,
                                CASE WHEN NOT st.higher_is_better THEN ubs.best_score_value END ASC,
                                ubs.earliest_date ASC
                        )::INTEGER as global_rank,
                        ubs.level_id,
                        ubs.level_version,
                        ubs.type_id as score_type,
                        ubs.best_score_value as best_score,
                        ubs.user_id,
                        COALESCE(u.display_name, '''') as display_name,
                        u.friend_code
                    FROM user_best_scores ubs
                    JOIN score_type st ON ubs.type_id = st.id
                    JOIN "user" u ON ubs.user_id = u.id
                )
                SELECT
                    global_rank as rank,
                    level_id,
                    level_version,
                    score_type,
                    best_score,
                    user_id,
                    display_name,
                    friend_code
                FROM ranked_scores
                ORDER BY global_rank
                OFFSET %s LIMIT %s
            ', v_final_where, p_offset, p_size);

        WHEN 'friends' THEN
            RAISE EXCEPTION 'Friends scope not yet implemented';

        WHEN 'regional' THEN
            RAISE EXCEPTION 'Regional scope not yet implemented';

        ELSE
            RAISE EXCEPTION 'Invalid scope: %', p_scope;
    END CASE;

    RETURN QUERY EXECUTE v_sql;
END;
$$ LANGUAGE plpgsql;
