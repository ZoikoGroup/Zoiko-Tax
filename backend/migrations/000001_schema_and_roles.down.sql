-- Local development only. See 000004's header.
--
-- The role is not dropped: it may hold grants in other databases on the same
-- instance, and dropping a role out from under them is a worse outcome than
-- leaving one behind in a development database.
DROP SCHEMA IF EXISTS ztax CASCADE;
