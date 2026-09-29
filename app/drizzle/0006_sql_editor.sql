CREATE TABLE "sql_folder" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"parent_id" text,
	"name" varchar(80) NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sql_run" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"sql" text NOT NULL,
	"elapsed_ms" integer,
	"row_count" integer,
	"error" text,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sql_snippet" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"folder_id" text,
	"name" varchar(200) NOT NULL,
	"sql_text" text NOT NULL,
	"favorite" boolean DEFAULT false NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "sql_folder" ADD CONSTRAINT "sql_folder_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "sql_run" ADD CONSTRAINT "sql_run_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "sql_snippet" ADD CONSTRAINT "sql_snippet_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "sql_snippet" ADD CONSTRAINT "sql_snippet_folder_id_sql_folder_id_fk" FOREIGN KEY ("folder_id") REFERENCES "public"."sql_folder"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "sql_folder_project_idx" ON "sql_folder" USING btree ("project_id");--> statement-breakpoint
CREATE INDEX "sql_folder_parent_idx" ON "sql_folder" USING btree ("parent_id");--> statement-breakpoint
CREATE UNIQUE INDEX "sql_folder_name_idx" ON "sql_folder" USING btree ("project_id","parent_id","name");--> statement-breakpoint
CREATE INDEX "sql_run_project_idx" ON "sql_run" USING btree ("project_id","created_at");--> statement-breakpoint
CREATE INDEX "sql_snippet_project_idx" ON "sql_snippet" USING btree ("project_id");--> statement-breakpoint
CREATE INDEX "sql_snippet_folder_idx" ON "sql_snippet" USING btree ("folder_id");--> statement-breakpoint
CREATE INDEX "sql_snippet_fav_idx" ON "sql_snippet" USING btree ("project_id","favorite");