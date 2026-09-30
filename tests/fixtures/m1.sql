CREATE TABLE public.groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE
);
CREATE TABLE public.items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  group_id bigint REFERENCES public.groups(id),
  name text NOT NULL UNIQUE,
  visible boolean NOT NULL DEFAULT false
);
ALTER TABLE public.groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.items ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON public.groups,public.items TO p___PROJECT_ID___anon;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.groups,public.items TO p___PROJECT_ID___service_role;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO p___PROJECT_ID___service_role;
CREATE POLICY anon_groups ON public.groups FOR SELECT TO p___PROJECT_ID___anon USING (true);
CREATE POLICY anon_items ON public.items FOR SELECT TO p___PROJECT_ID___anon USING (visible);
CREATE POLICY service_groups ON public.groups FOR ALL TO p___PROJECT_ID___service_role USING (true) WITH CHECK (true);
CREATE POLICY service_items ON public.items FOR ALL TO p___PROJECT_ID___service_role USING (true) WITH CHECK (true);
CREATE FUNCTION public.add_numbers(a integer,b integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT a+b';
REVOKE ALL ON FUNCTION public.add_numbers(integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.add_numbers(integer,integer) TO p___PROJECT_ID___anon;
