<?php
declare(strict_types=1);

// Export a stable Flarum migration snapshot. The DB connection is put into a
// read-only, repeatable-read transaction before any source rows are queried.
// The result contains private account data and password hashes: store it only
// in a protected directory and delete it after the staging import is accepted.

if ($argc !== 3) {
    fwrite(STDERR, "Usage: php flarum_snapshot.php /path/to/flarum/config.php /secure/path/snapshot.json\n");
    exit(2);
}

$configPath = $argv[1];
$outputPath = $argv[2];
if (!is_file($configPath) || !is_readable($configPath)) {
    fwrite(STDERR, "Flarum config is not readable.\n");
    exit(2);
}
if (file_exists($outputPath) || is_link($outputPath)) {
    fwrite(STDERR, "Refusing to overwrite an existing snapshot path.\n");
    exit(2);
}
$directory = dirname($outputPath);
if (!is_dir($directory) || !is_writable($directory)) {
    fwrite(STDERR, "Snapshot directory must already exist and be writable.\n");
    exit(2);
}

$openedOutput = false;
try {
    // Config is executed by Flarum's normal deployment model; never print it.
    $config = require $configPath;
    if (!isset($config['database']) || !is_array($config['database'])) {
        throw new RuntimeException('Missing Flarum database configuration.');
    }
    $db = $config['database'];
    foreach (['host', 'database', 'username', 'password'] as $required) {
        if (!array_key_exists($required, $db)) {
            throw new RuntimeException('Incomplete Flarum database configuration.');
        }
    }
    $prefix = (string)($db['prefix'] ?? '');
    if (!preg_match('/^[A-Za-z0-9_]*$/D', $prefix)) {
        throw new RuntimeException('Unsafe database table prefix.');
    }
    $port = (int)($db['port'] ?? 3306);
    $charset = (string)($db['charset'] ?? 'utf8mb4');
    if (!preg_match('/^[A-Za-z0-9_]+$/D', $charset)) {
        throw new RuntimeException('Unsafe database charset.');
    }

    $dsn = sprintf('mysql:host=%s;port=%d;dbname=%s;charset=%s', $db['host'], $port, $db['database'], $charset);
    $pdo = new PDO($dsn, (string)$db['username'], (string)$db['password'], [
        PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
        PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
        PDO::ATTR_EMULATE_PREPARES => false,
    ]);
    // These are session/transaction controls, not database mutations.
    $pdo->exec('SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ');
    $pdo->exec('SET SESSION TRANSACTION READ ONLY');
    $pdo->exec('START TRANSACTION WITH CONSISTENT SNAPSHOT');

    $tables = [
        'users' => ['id', 'username', 'email', 'is_email_confirmed', 'password', 'avatar_url', 'joined_at', 'last_seen_at', 'suspended_until', 'suspend_reason'],
        'groups' => ['id', 'name_singular', 'name_plural', 'color', 'icon', 'is_hidden', 'created_at'],
        'group_user' => ['user_id', 'group_id', 'created_at'],
        'group_permission' => ['group_id', 'permission', 'created_at'],
        'discussions' => ['id', 'title', 'comment_count', 'created_at', 'user_id', 'first_post_id', 'last_posted_at', 'last_posted_user_id', 'last_post_id', 'hidden_at', 'slug', 'is_private', 'is_approved', 'is_sticky', 'is_locked'],
        'posts' => ['id', 'discussion_id', 'number', 'created_at', 'user_id', 'type', 'content', 'edited_at', 'edited_user_id', 'hidden_at', 'hidden_user_id', 'is_private', 'is_approved'],
        'tags' => ['id', 'name', 'slug', 'description', 'color', 'position', 'parent_id', 'is_restricted', 'is_hidden', 'created_at'],
        'discussion_tag' => ['discussion_id', 'tag_id', 'created_at'],
        'discussion_user' => ['user_id', 'discussion_id', 'last_read_at', 'last_read_post_number', 'subscription'],
        'post_user' => ['post_id', 'user_id'],
        'post_mentions_group' => ['post_id', 'mentions_group_id', 'created_at'],
        'post_mentions_post' => ['post_id', 'mentions_post_id', 'created_at'],
        'post_mentions_tag' => ['post_id', 'mentions_tag_id', 'created_at'],
        'post_mentions_user' => ['post_id', 'mentions_user_id', 'created_at'],
        'tag_user' => ['user_id', 'tag_id', 'marked_as_read_at', 'is_hidden'],
        'post_likes' => ['post_id', 'user_id', 'created_at'],
        'flags' => ['id', 'post_id', 'type', 'user_id', 'reason', 'reason_detail', 'created_at'],
        'notifications' => ['id', 'user_id', 'from_user_id', 'type', 'subject_id', 'data', 'created_at', 'is_deleted', 'read_at'],
    ];
    $blobFields = ['notifications' => ['data']];

    umask(0077);
    $output = fopen($outputPath, 'x');
    if ($output === false) {
        throw new RuntimeException('Could not create a new snapshot file.');
    }
    $openedOutput = true;
    if (!chmod($outputPath, 0600)) {
        throw new RuntimeException('Could not secure snapshot file permissions.');
    }
    $write = static function (string $text) use ($output): void {
        $length = strlen($text);
        for ($written = 0; $written < $length;) {
            $n = fwrite($output, substr($text, $written));
            if ($n === false || $n === 0) {
                throw new RuntimeException('Could not write the snapshot file.');
            }
            $written += $n;
        }
    };

    $write('{"format_version":1,"source":"flarum","tables":{');
    $firstTable = true;
    $counts = [];
    foreach ($tables as $table => $columns) {
        $qualified = '`' . $prefix . $table . '`';
        $columnSQL = implode(',', array_map(static fn(string $column): string => '`' . $column . '`', $columns));
        $count = (int)$pdo->query('SELECT COUNT(*) FROM ' . $qualified)->fetchColumn();
        $statement = $pdo->query('SELECT ' . $columnSQL . ' FROM ' . $qualified . ' ORDER BY ' . (isset($columns[0]) ? '`' . $columns[0] . '`' : '1'));
        if (!$firstTable) {
            $write(',');
        }
        $firstTable = false;
        $write(json_encode($table, JSON_THROW_ON_ERROR) . ':{"source_count":' . $count . ',"rows":[');
        $firstRow = true;
        while (($row = $statement->fetch()) !== false) {
            foreach ($blobFields[$table] ?? [] as $field) {
                if ($row[$field] !== null) {
                    $row[$field] = ['base64' => base64_encode((string)$row[$field])];
                }
            }
            if (!$firstRow) {
                $write(',');
            }
            $firstRow = false;
            $write(json_encode($row, JSON_THROW_ON_ERROR | JSON_INVALID_UTF8_SUBSTITUTE));
        }
        $statement->closeCursor();
        $write(']}');
        $counts[$table] = $count;
    }
    $write('},"generated_at":' . json_encode(gmdate(DATE_ATOM), JSON_THROW_ON_ERROR) . "}\n");
    if (!fflush($output)) {
        throw new RuntimeException('Could not flush the snapshot file.');
    }
    fclose($output);
    $output = null;
    $pdo->commit();
    $summary = [];
    foreach ($counts as $table => $count) {
        $summary[] = $table . '=' . $count;
    }
    fwrite(STDOUT, 'Snapshot created with mode 0600; source row counts: ' . implode(', ', $summary) . "\n");
} catch (Throwable $error) {
    if (isset($pdo) && $pdo instanceof PDO && $pdo->inTransaction()) {
        $pdo->rollBack();
    }
    if (isset($output) && is_resource($output)) {
        fclose($output);
    }
    if ($openedOutput) {
        @unlink($outputPath);
    }
    fwrite(STDERR, 'Snapshot failed: ' . $error->getMessage() . "\n");
    exit(1);
}
