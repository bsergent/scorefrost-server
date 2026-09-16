-- Fetch statistics for a specific level and user
SELECT
  sol.level_id AS id, sol.level_version as ver,
  ROUND(MIN(scr.score)::numeric, 1) AS min_val,
  ROUND(PERCENTILE_CONT(0.25) WITHIN GROUP (ORDER BY scr.score)::numeric, 1) AS q1_val,
  ROUND(AVG(scr.score)::numeric, 1) AS avg_val,
  ROUND(PERCENTILE_CONT(0.75) WITHIN GROUP (ORDER BY scr.score)::numeric, 1) AS q3_val,
  ROUND(MAX(scr.score)::numeric, 1) AS max_val,
  ROUND(STDDEV(scr.score)::numeric, 2) AS std_dev,
  ROUND(PERCENTILE_CONT(0.85) WITHIN GROUP (ORDER BY scr.score)::numeric, 1) AS p85_val
FROM public.solution AS sol
JOIN public.user AS usr ON sol.user_id = usr.id
JOIN public.score AS scr ON sol.id = scr.solution_id
JOIN public.score_type AS scrt ON scr.type_id = scrt.id
WHERE usr.display_name = 'Quick Glacier'
  AND scrt.display_name = 'Striping'
GROUP BY 
  sol.level_id,
  sol.level_version

-- List attempts for a specific level and user
SELECT
  sol.level_id AS id, sol.level_version as ver, scr.score as val
FROM public.solution AS sol
JOIN public.user AS usr ON sol.user_id = usr.id
JOIN public.score AS scr ON sol.id = scr.solution_id
JOIN public.score_type AS scrt ON scr.type_id = scrt.id
WHERE usr.display_name = 'Quick Glacier'
  AND scrt.display_name = 'Striping'
  AND sol.level_id = '1982292a'
