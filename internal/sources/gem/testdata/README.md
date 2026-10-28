# Gem fixture

`board.json` is two real posts, fetched 2026-10-27 from
`GET https://api.gem.com/job_board/v0/groq/job_posts/` (a Dallas in-office
post that states a pay range) and `.../retool/job_posts/` (a remote post with
no pay). Every field is as served. Only the long description text was cut:
`content_plain` keeps its first four lines and the lines that state pay;
`content` keeps its first four `<div>` blocks and the blocks that state pay.
No e-mail address or phone number is in the kept text.
