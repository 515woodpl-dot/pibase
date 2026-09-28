CREATE TABLE "backup" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"kind" text NOT NULL,
	"status" text DEFAULT 'running' NOT NULL,
	"storage_key" text,
	"size_bytes" bigint,
	"error" text,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"finished_at" timestamp
);
--> statement-breakpoint
ALTER TABLE "backup" ADD CONSTRAINT "backup_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "backup_project_idx" ON "backup" USING btree ("project_id","created_at");--> statement-breakpoint
CREATE UNIQUE INDEX "backup_one_running_idx" ON "backup" USING btree ("project_id") WHERE status = 'running';