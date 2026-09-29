CREATE TABLE "sql_editor_state" (
	"project_id" text PRIMARY KEY NOT NULL,
	"starters_created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
DROP INDEX "sql_folder_name_idx";--> statement-breakpoint
ALTER TABLE "sql_editor_state" ADD CONSTRAINT "sql_editor_state_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
-- Folders whose parent was deleted before the parent link cascaded: remove them (and, by the
-- snippet cascade, their snippets) so the new foreign key can be added. Keeps every folder that
-- still reaches a top-level folder.
WITH RECURSIVE reachable AS (
	SELECT "id" FROM "sql_folder" WHERE "parent_id" IS NULL
	UNION
	SELECT f."id" FROM "sql_folder" f JOIN reachable r ON f."parent_id" = r."id"
)
DELETE FROM "sql_folder" WHERE "id" NOT IN (SELECT "id" FROM reachable);--> statement-breakpoint
ALTER TABLE "sql_folder" ADD CONSTRAINT "sql_folder_parent_id_sql_folder_id_fk" FOREIGN KEY ("parent_id") REFERENCES "public"."sql_folder"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
-- Same-name folders in one place (possible before this index at the top level, or differing only
-- in case): keep the first, rename the others so the unique index can be created.
UPDATE "sql_folder" f SET "name" = left(f."name", 70) || ' (' || left(f."id", 6) || ')'
WHERE EXISTS (
	SELECT 1 FROM "sql_folder" g
	WHERE g."project_id" = f."project_id" AND coalesce(g."parent_id", '') = coalesce(f."parent_id", '')
		AND lower(g."name") = lower(f."name") AND g."id" < f."id"
);--> statement-breakpoint
CREATE UNIQUE INDEX "sql_folder_unique_name_idx" ON "sql_folder" USING btree ("project_id",coalesce("parent_id", ''),lower("name"));